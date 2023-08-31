package bdd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/cucumber/godog"
	"github.com/cucumber/godog/colors"
	"github.com/google/uuid"
	"github.com/spf13/pflag"
	"github.com/tigh-latte/go-bdd/bddcontext"
	"github.com/tigh-latte/go-bdd/config"
	"github.com/tigh-latte/go-bdd/fake"
	configinternal "github.com/tigh-latte/go-bdd/internal/config"
	"github.com/zeroflucs-given/generics/collections/stack"
)

var godogOpts = godog.Options{
	Output: colors.Colored(os.Stdout),
	Format: "progress",
	FS:     os.DirFS("."),
	Paths:  []string{"features"},
	Strict: true,
}

func init() {
	godog.BindCommandLineFlags("godog.", &godogOpts)

	configinternal.BindFlags()

	pflag.Parse()
}

type Suite struct {
	suite  godog.TestSuite
	reqFns RequireFuncs
}

func NewSuite(name string, oo ...TestSuiteOptionFunc) *Suite {
	godogOpts.Paths = pflag.Args()

	opts := &testSuiteOpts{
		customBeforeSuiteFunc: noopTestSuiteHook,
		customAfterSuiteFunc:  noopTestSuiteHook,

		customBeforeScenarioFunc: noopBeforeScenarioHook,
		customAfterScenarioFunc:  noopAfterScenarioHook,

		customBeforeStepFunc: noopTestBeforeStepHook,
		customAfterStepFunc:  noopTestAfterStepHook,

		customStepFunc:      []TestCustomStepFunc{},
		customRequireFuncs:  RequireFuncs{},
		customTemplateFuncs: make(template.FuncMap),

		cookies:      make([]*http.Cookie, 0),
		alwaysIgnore: make([]string, 0),
	}
	for _, o := range oo {
		o(opts)
	}

	s := &Suite{
		reqFns: make(RequireFuncs, 0),
		suite: godog.TestSuite{
			Name:    name,
			Options: &godogOpts,
		},
	}
	s.suite.TestSuiteInitializer = s.initSuite(opts)
	s.suite.ScenarioInitializer = s.initScenario(opts)

	return s
}

func (s *Suite) Run() int {
	return s.suite.Run()
}

func (s *Suite) initSuite(opts *testSuiteOpts) func(ctx *godog.TestSuiteContext) {
	return func(ctx *godog.TestSuiteContext) {
		ctx.BeforeSuite(func() {
			config.Init()
			opts.applyConfig()

			if opts.s3 != nil {
				if err := initS3(opts.s3); err != nil {
					panic(err)
				}
			}
		})

		ctx.BeforeSuite(func() {
			for k, v := range opts.customRequireFuncs {
				if _, ok := s.reqFns[k]; ok {
					panic("cannot overwrite builtin require function")
				}
				s.reqFns[k] = v
			}
		})

		ctx.BeforeSuite(func() {
			if config.IsDryRun() {
				return
			}

			// perform rabbitmq connection check here
		})

		ctx.BeforeSuite(opts.customBeforeSuiteFunc)

		ctx.AfterSuite(opts.customAfterSuiteFunc)

		ctx.AfterSuite(func() {
			// shut down services here
		})
	}
}

func (s *Suite) initScenario(opts *testSuiteOpts) func(ctx *godog.ScenarioContext) {
	return func(ctx *godog.ScenarioContext) {
		sd := &bddcontext.Context{
			TemplateValues: make(map[string]any),
			S3Client:       s3Client,
			HTTP: &bddcontext.HTTPContext{
				Headers:     make(http.Header, 0),
				Cookies:     make([]*http.Cookie, len(opts.cookies)),
				Requests:    stack.NewStack[json.RawMessage](0),
				Responses:   stack.NewStack[json.RawMessage](0),
				TestData:    opts.httpDataDir,
				QueryParams: make(url.Values),
				ToIgnore:    make([]string, 0),
				Client:      &http.Client{Timeout: 30 * time.Second},
			},
			TestData: opts.testDataDir,
			Template: template.New("template"),

			IgnoreAlways: make([]string, len(opts.alwaysIgnore)),
		}
		ctx.Before(func(ctx context.Context, sn *godog.Scenario) (context.Context, error) {
			now := time.Now()

			today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
			yesterday := time.Date(now.Year(), now.Month(), now.Day()-1, 0, 0, 0, 0, now.Location())
			tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())

			sd.ID = sn.Id
			sd.Time = now
			sd.TemplateValues["__scenario_id"] = sn.Id
			sd.TemplateValues["__unix_time"] = sd.Time.UnixMilli()
			sd.TemplateValues["__now"] = now.String()
			sd.TemplateValues["__today"] = today.Local().Format("2006-01-02")
			sd.TemplateValues["__today_timestamp"] = today.Local().String()
			sd.TemplateValues["__yesterday"] = yesterday.Local().Format("2006-01-02")
			sd.TemplateValues["__yesterday_timestamp"] = yesterday.Local().String()
			sd.TemplateValues["__tomorrow"] = tomorrow.Local().Format("2006-01-02")
			sd.TemplateValues["__tomorrow_timestamp"] = tomorrow.Local().String()

			sd.Template.Funcs(template.FuncMap{
				"date_add": func(year, month, days int) string {
					return today.AddDate(year, month, days).Format("2006-01-02")
				},
				"random_name":       fake.Name,
				"random_first_name": fake.FirstName,
				"random_last_name":  fake.LastName,
				"random_email":      fake.Email,
				"random_sentence":   fake.Sentence,
				"upper":             strings.ToUpper,
				"lower":             strings.ToLower,
				"intify":            intify,
				"uuid": func() string {
					return uuid.New().String()
				},
			})
			sd.Template.Funcs(opts.customTemplateFuncs)

			if opts.s3 != nil {
				// init context
			}
			if opts.rmq != nil {
				// for rabbitmq
			}
			if opts.ws != nil {
				sd.WS = &bddcontext.WebsocketContext{
					Host:    "TODO",
					Timeout: opts.ws.Timeout,
					Connections: make(map[string]struct {
						SessionID string
						Messages  *stack.Stack[[]byte]
					}, 0),
					TestData: opts.wsDataDir,
					Client:   opts.ws.Client,
				}
			}
			copy(sd.HTTP.Cookies, opts.cookies)
			copy(sd.IgnoreAlways, opts.alwaysIgnore)

			return bddcontext.WithContext(ctx, sd), nil
		})
		ctx.Before(func(ctx context.Context, sn *godog.Scenario) (context.Context, error) {
			var err error
			for _, tag := range sn.Tags {
				fnName, ok := strings.CutPrefix(tag.Name, TagRequire)
				if !ok {
					continue
				}
				fn, ok := s.reqFns[fnName]
				if !ok {
					return ctx, errors.New("unregistered required func")
				}
				if ctx, err = fn(ctx); err != nil {
					return ctx, err
				}
			}

			return ctx, nil
		})
		ctx.Before(func(ctx context.Context, sn *godog.Scenario) (context.Context, error) {
			return opts.customBeforeScenarioFunc(ctx, sn)
		})

		ctx.StepContext().Before(func(ctx context.Context, st *godog.Step) (context.Context, error) {
			// Whatever we want to do can be added here.
			return ctx, nil
		})
		ctx.StepContext().Before(func(ctx context.Context, st *godog.Step) (context.Context, error) {
			return opts.customBeforeStepFunc(ctx, st)
		})

		ctx.StepContext().After(func(ctx context.Context, st *godog.Step, status godog.StepResultStatus, err error) (context.Context, error) {
			return opts.customAfterStepFunc(ctx, st, status, err)
		})

		ctx.StepContext().After(func(ctx context.Context, st *godog.Step, status godog.StepResultStatus, err error) (context.Context, error) {
			// Whatever we want to do can be added here.
			return ctx, nil
		})
		ctx.After(func(ctx context.Context, sn *godog.Scenario, err error) (context.Context, error) {
			return opts.customAfterScenarioFunc(ctx, sn, err)
		})
		ctx.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
			if err == nil {
				return ctx, nil
			}

			fmt.Println(sd)
			fmt.Printf("fake.Seed=%s\n", fake.GetInfo())

			return ctx, nil
		})
		ctx.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
			if opts.ws != nil {
				for _, connection := range sd.WS.Connections {
					sd.WS.Client.Close(ctx, connection.SessionID)
				}
			}

			return ctx, nil
		})

		adder := &stepAdder{ctx: ctx}
		initSteps(adder)

		for _, fn := range opts.customStepFunc {
			fn(adder)
		}
	}
}