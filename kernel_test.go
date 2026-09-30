package kernel_test

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gocrud/kernel"
	config "github.com/gocrud/kernel/config"
)

// ---------- fixtures ----------

type Logger interface{ Log(string) }
type consoleLogger struct{ prefix string }

func (c *consoleLogger) Log(msg string) {}
func NewConsoleLogger() *consoleLogger  { return &consoleLogger{prefix: "console"} }

type Repository struct{ Logger Logger }

func NewRepository(logger Logger) *Repository { return &Repository{Logger: logger} }

type Validator interface{ Validate() string }
type emailValidator struct{}

func (emailValidator) Validate() string { return "email" }

type phoneValidator struct{}

func (phoneValidator) Validate() string { return "phone" }

// upperLogger decorates an inner Logger for the Decorate test.
type upperLogger struct{ inner Logger }

func (u *upperLogger) Log(msg string) { u.inner.Log(msg) }

// circA / circB form a dependency cycle.
type circA struct{ B *circB }
type circB struct{ A *circA }

// ---------- DI core ----------

func TestAutoWiring(t *testing.T) {
	b := kernel.New()
	b.Provide[Logger](NewConsoleLogger).
		Provide[*Repository](NewRepository)

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}

	l1 := app.MustGet[Logger]()
	l2 := app.MustGet[Logger]()
	if l1 != l2 {
		t.Fatal("expected singleton logger to be the same instance")
	}
	r1 := app.MustGet[*Repository]()
	r2 := app.MustGet[*Repository]()
	if r1 != r2 {
		t.Fatal("expected singleton repository to be the same instance")
	}
	if r1.Logger != l1 {
		t.Fatal("expected auto-wired ctor to receive the singleton logger")
	}
}

func TestGetAll(t *testing.T) {
	b := kernel.New()
	b.Provide[Validator](func() Validator { return emailValidator{} })
	b.Provide[Validator](func() Validator { return phoneValidator{} })

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	all, err := app.GetAll[Validator]()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Validate() != "email" || all[1].Validate() != "phone" {
		t.Fatalf("unexpected GetAll result: %#v", all)
	}
	if last := app.MustGet[Validator](); last.Validate() != "phone" {
		t.Fatalf("expected Get to return the last registration, got %s", last.Validate())
	}
}

func TestTryProvide(t *testing.T) {
	b := kernel.New()
	b.Provide[Logger](NewConsoleLogger)
	b.TryProvide[Logger](func() Logger { return &consoleLogger{prefix: "skipped"} })

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := app.MustGet[Logger]().(*consoleLogger).prefix; got != "console" {
		t.Fatalf("expected TryProvide to skip, got prefix %q", got)
	}
}

func TestProviderInjection(t *testing.T) {
	b := kernel.New()
	b.Provide[Logger](NewConsoleLogger)
	// A user-provided Provider[Logger] wins over the auto-registered one: the
	// zero value has no factory, so Get reports ErrServiceNotRegistered instead
	// of returning the singleton the auto provider would return.
	b.Provide[kernel.Provider[Logger]](kernel.Provider[Logger]{})

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.MustGet[kernel.Provider[Logger]]().Get(); !errors.Is(err, kernel.ErrServiceNotRegistered) {
		t.Fatalf("expected the user-provided provider to win, got %v", err)
	}
}

func TestProviderCtorInjection(t *testing.T) {
	type Holder struct{ Get Logger }

	b := kernel.New()
	b.Provide[Logger](NewConsoleLogger).
		Provide[*Holder](func(p kernel.Provider[Logger]) *Holder {
		return &Holder{Get: p.MustGet()}
	})

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if app.MustGet[*Holder]().Get != app.MustGet[Logger]() {
		t.Fatal("Provider[T] inside a constructor must return the singleton")
	}
}

func TestProviderCycleDetected(t *testing.T) {
	type A struct{ P kernel.Provider[*A] }

	b := kernel.New()
	b.Provide[*A](func(p kernel.Provider[*A]) *A { return &A{P: p} })

	_, err := b.Build()
	var circ *kernel.CircularDependencyError
	if !errors.As(err, &circ) {
		t.Fatalf("expected CircularDependencyError, got %v", err)
	}
}

type Cache interface{ Driver() string }
type redisCache struct{}

func (redisCache) Driver() string { return "redis" }

type memoryCache struct{}

func (memoryCache) Driver() string { return "memory" }

func TestKeyed(t *testing.T) {
	b := kernel.New()
	b.ProvideKeyed[Cache]("redis", func() Cache { return redisCache{} })
	b.ProvideKeyed[Cache]("memory", func() Cache { return memoryCache{} })
	b.TryProvideKeyed[Cache]("redis", func() Cache { return memoryCache{} }) // skipped

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := app.MustGetKeyed[Cache]("redis").Driver(); got != "redis" {
		t.Fatalf("expected redis, got %s", got)
	}
	if _, err := app.GetKeyed[Cache]("nope"); !errors.Is(err, kernel.ErrServiceNotRegistered) {
		t.Fatalf("expected ErrServiceNotRegistered, got %v", err)
	}
	all := app.GetAllKeyed[Cache]("redis")
	if len(all) != 1 || all[0].Driver() != "redis" {
		t.Fatalf("unexpected GetAllKeyed result: %#v", all)
	}
	// keyed and unkeyed are independent spaces
	if _, err := app.Get[Cache](); !errors.Is(err, kernel.ErrServiceNotRegistered) {
		t.Fatal("keyed registrations must not be visible unkeyed")
	}
}

func TestKeyedAccessorInjection(t *testing.T) {
	type Router struct{ Primary Cache }

	b := kernel.New()
	b.ProvideKeyed[Cache]("redis", func() Cache { return redisCache{} })
	b.ProvideKeyed[Cache]("memory", func() Cache { return memoryCache{} })
	b.Provide[*Router](func(k kernel.Keyed[Cache]) *Router {
		return &Router{Primary: k.MustGet("memory")}
	})

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := app.MustGet[*Router]().Primary.Driver(); got != "memory" {
		t.Fatalf("expected memory cache, got %s", got)
	}
	k := app.MustGet[kernel.Keyed[Cache]]()
	if got := k.All("redis"); len(got) != 1 || got[0].Driver() != "redis" {
		t.Fatal("Keyed.All must resolve the keyed registrations")
	}
}

func TestDecorate(t *testing.T) {
	b := kernel.New()
	b.Provide[Logger](NewConsoleLogger)
	b.Decorate[Logger](func(inner Logger) (Logger, error) {
		return &upperLogger{inner: inner}, nil
	})

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := app.MustGet[Logger]().(*upperLogger); !ok {
		t.Fatalf("expected decorated logger, got %T", app.MustGet[Logger]())
	}
}

func TestExtend(t *testing.T) {
	// An Extension takes only *kernel.AppBuilder and returns nothing.
	ext := func(b *kernel.AppBuilder) {
		b.TryProvide[Validator](func() Validator { return emailValidator{} })
	}

	b := kernel.New()
	// Extend returns the same builder, so chaining continues; the second call
	// is a no-op because the extension registers via TryProvide.
	b.Extend(ext).
		Extend(ext).
		Provide[Logger](NewConsoleLogger)

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := app.MustGet[Validator]().Validate(); got != "email" {
		t.Fatalf("expected extension to register the validator, got %s", got)
	}
	if app.MustGet[Logger]() == nil {
		t.Fatal("expected registrations after Extend to land on the same builder")
	}
}

func TestCircularDependency(t *testing.T) {
	b := kernel.New()
	b.Provide[*circA](func(bb *circB) *circA { return &circA{B: bb} })
	b.Provide[*circB](func(aa *circA) *circB { return &circB{A: aa} })

	_, err := b.Build()
	var circ *kernel.CircularDependencyError
	if !errors.As(err, &circ) {
		t.Fatalf("expected CircularDependencyError, got %v", err)
	}
	if len(circ.Chain) < 2 {
		t.Fatalf("expected a chain, got %v", circ.Chain)
	}
}

func TestMissingDependency(t *testing.T) {
	b := kernel.New()
	b.Provide[*Repository](NewRepository) // Logger never registered

	_, err := b.Build()
	if !errors.Is(err, kernel.ErrServiceNotRegistered) {
		t.Fatalf("expected ErrServiceNotRegistered, got %v", err)
	}
}

// ---------- options ----------

type AppConfig struct {
	Port int
	Host string
}

func TestConfigure(t *testing.T) {
	b := kernel.New()
	b.Configure(func(c *AppConfig) { c.Port = 8080 })
	b.Configure(func(c *AppConfig) { c.Port++ })

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := app.MustGet[*kernel.Config[AppConfig]]().Value.Port; got != 8081 {
		t.Fatalf("expected merged port 8081, got %d", got)
	}
}

func TestConfigureMonitor(t *testing.T) {
	b := kernel.New()
	b.ConfigureMonitor(func(c *AppConfig) { c.Port = 8080 })

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	m := app.MustGet[*kernel.ConfigMonitor[AppConfig]]()
	if got := m.CurrentValue().Port; got != 8080 {
		t.Fatalf("expected initial port 8080, got %d", got)
	}
	m.Set(AppConfig{Port: 9090})
	if got := m.CurrentValue().Port; got != 9090 {
		t.Fatalf("expected port 9090 after Set, got %d", got)
	}
}

// ---------- lifecycle ----------

type orderRecorder struct {
	events []string
}

type serviceA struct{ r *orderRecorder }

func (a *serviceA) Start(ctx context.Context) error {
	a.r.events = append(a.r.events, "start:A")
	return nil
}
func (a *serviceA) Stop(ctx context.Context) error {
	a.r.events = append(a.r.events, "stop:A")
	return nil
}

type serviceB struct{ r *orderRecorder }

func (b *serviceB) Stop(ctx context.Context) error {
	b.r.events = append(b.r.events, "stop:B")
	return nil
}

type closerC struct{ r *orderRecorder }

func (c *closerC) Close() error { c.r.events = append(c.r.events, "close:C"); return nil }

func TestLifecycleOrder(t *testing.T) {
	rec := &orderRecorder{}
	b := kernel.New()
	b.Provide[*orderRecorder](rec)
	b.Provide[*serviceA](func(r *orderRecorder) *serviceA { r.events = append(r.events, "new:A"); return &serviceA{r: r} })
	b.Provide[*serviceB](func(r *orderRecorder) *serviceB { r.events = append(r.events, "new:B"); return &serviceB{r: r} })
	b.Provide[*closerC](func(r *orderRecorder) *closerC { r.events = append(r.events, "new:C"); return &closerC{r: r} }, kernel.WithClose())

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Construction order among A/B/C is unspecified, but Stop must dispose them
	// in the exact reverse of construction order.
	var built, stopped []string
	for _, e := range rec.events {
		switch {
		case strings.HasPrefix(e, "new:"):
			built = append(built, e)
		case e == "start:A":
			// startup happens after construction, before any stop
		case strings.HasPrefix(e, "stop:") || strings.HasPrefix(e, "close:"):
			stopped = append(stopped, e)
		}
	}
	if len(built) != 3 || len(stopped) != 3 {
		t.Fatalf("unexpected events: %v", rec.events)
	}
	for i := range built {
		want := strings.Replace(strings.Replace(built[len(built)-1-i], "new", "stop", 1), "stop:C", "close:C", 1)
		if stopped[i] != want {
			t.Fatalf("stop order must be reverse of construction order: built=%v stopped=%v", built, stopped)
		}
	}
}

func TestCloserNotAutoClosed(t *testing.T) {
	rec := &orderRecorder{}
	b := kernel.New()
	b.Provide[*orderRecorder](rec)
	b.Provide[*closerC](func(r *orderRecorder) *closerC { return &closerC{r: r} })

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Without kernel.WithClose, an io.Closer singleton must be ignored: Close
	// must never be called (embedded fields like *sql.DB must not be closed
	// just because they satisfy io.Closer).
	for _, e := range rec.events {
		if strings.HasPrefix(e, "close:") {
			t.Fatalf("Close was called without WithClose: %v", rec.events)
		}
	}
}

// ---------- Run ----------

type chanService struct {
	started chan struct{}
	stopped chan struct{}
}

func (s *chanService) Start(ctx context.Context) error { close(s.started); return nil }
func (s *chanService) Stop(ctx context.Context) error  { close(s.stopped); return nil }

func TestRunStartsWaitsAndStops(t *testing.T) {
	svc := &chanService{started: make(chan struct{}), stopped: make(chan struct{})}
	b := kernel.New()
	b.Provide[*chanService](svc)

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	select {
	case <-svc.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not start services")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error on graceful shutdown: %v", err)
	}
	select {
	case <-svc.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop services")
	}
}

type errStartService struct{}

func (errStartService) Start(ctx context.Context) error { return errors.New("boom") }

func TestRunReturnsStartError(t *testing.T) {
	b := kernel.New()
	b.Provide[*errStartService](&errStartService{})

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	// Start fails, so Run must return immediately without blocking.
	if err := app.Run(context.Background()); err == nil {
		t.Fatal("expected Run to surface the Start error")
	}
}

type errStopService struct{}

func (errStopService) Stop(ctx context.Context) error { return errors.New("stop boom") }

func TestRunReturnsStopError(t *testing.T) {
	b := kernel.New()
	b.Provide[*errStopService](&errStopService{})

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := app.Run(ctx); err == nil {
		t.Fatal("expected Run to surface the Stop error")
	}
}

// ---------- config ----------

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

type Repository2 struct{ Cfg *kernel.Config[AppConfig] }

func NewRepository2(cfg *kernel.Config[AppConfig]) *Repository2 { return &Repository2{Cfg: cfg} }

func TestConfigStaticYAML(t *testing.T) {
	path := writeTemp(t, "app.yaml", "app:\n  port: 8080\n  host: localhost\n")

	b := kernel.New()
	b.Config[AppConfig]("app", config.WithFile(path))
	b.Provide[*Repository2](NewRepository2)

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	got := app.MustGet[*kernel.Config[AppConfig]]().Value
	if got.Port != 8080 || got.Host != "localhost" {
		t.Fatalf("unexpected config: %+v", got)
	}
}

func TestConfigPrecedence(t *testing.T) {
	path := writeTemp(t, "app.yaml", "app:\n  port: 2000\n")
	t.Setenv("APP_PORT", "6000")

	b := kernel.New()
	b.Config[AppConfig]("app",
		config.WithDefaults(func(c *AppConfig) { c.Port = 1000; c.Host = "default-host" }),
		config.WithFile(path),
		config.WithMap(map[string]any{"app.port": 4000}),
		config.WithEnv("APP_"),
	)

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	got := app.MustGet[*kernel.Config[AppConfig]]().Value
	if got.Port != 6000 {
		t.Fatalf("expected env to win with 6000, got %d", got.Port)
	}
	if got.Host != "default-host" {
		t.Fatalf("expected default preserved, got %q", got.Host)
	}
}

func TestConfigFlagPrecedence(t *testing.T) {
	path := writeTemp(t, "app.yaml", "app:\n  port: 2000\n")
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("app.port", "", "")
	if err := fs.Set("app.port", "7000"); err != nil {
		t.Fatal(err)
	}

	b := kernel.New()
	b.Config[AppConfig]("app", config.WithFile(path), config.WithFlag(fs))
	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := app.MustGet[*kernel.Config[AppConfig]]().Value.Port; got != 7000 {
		t.Fatalf("expected flag to win with 7000, got %d", got)
	}
}

func TestConfigTomlAndJSON(t *testing.T) {
	tomlPath := writeTemp(t, "app.toml", "[app]\nport = 8080\nhost = \"toml-host\"\n")
	jsonPath := writeTemp(t, "app.json", `{"app": {"port": 8081}}`)

	b := kernel.New()
	b.Config[AppConfig]("app", config.WithFile(tomlPath))
	b2 := kernel.New()
	b2.Config[AppConfig]("app", config.WithFile(jsonPath))

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := app.MustGet[*kernel.Config[AppConfig]]().Value; got.Port != 8080 || got.Host != "toml-host" {
		t.Fatalf("unexpected toml config: %+v", got)
	}
	app2, err := b2.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := app2.MustGet[*kernel.Config[AppConfig]]().Value; got.Port != 8081 || got.Host != "" {
		t.Fatalf("unexpected json config: %+v", got)
	}
}

func TestConfigBindingError(t *testing.T) {
	path := writeTemp(t, "app.yaml", "app:\n  port: not-a-number\n")

	b := kernel.New()
	b.Config[AppConfig]("app", config.WithFile(path))
	_, err := b.Build()
	var cfgErr *kernel.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected ConfigError, got %v", err)
	}
}

func TestConfigReloadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte("app:\n  port: 1000\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := kernel.New()
	b.Config[AppConfig]("app", config.WithFile(path), config.WithReloadable)
	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())

	mon := app.MustGet[*kernel.ConfigMonitor[AppConfig]]()
	if got := mon.CurrentValue().Port; got != 1000 {
		t.Fatalf("expected initial 1000, got %d", got)
	}
	if _, err := app.Get[*kernel.ConfigMonitor[AppConfig]](); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond) // let the fsnotify watch become active
	if err := os.WriteFile(path, []byte("app:\n  port: 2000\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if mon.CurrentValue().Port == 2000 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("config did not reload, port=%d", mon.CurrentValue().Port)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestConfigReloadKeepsOldOnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte("app:\n  port: 1000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var reloadErrs []error
	b := kernel.New()
	b.Config[AppConfig]("app", config.WithFile(path), config.WithReloadable, config.WithOnReloadError(func(err error) {
		reloadErrs = append(reloadErrs, err)
	}))
	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())

	mon := app.MustGet[*kernel.ConfigMonitor[AppConfig]]()
	// Break the file: reload must fail and keep the old value.
	time.Sleep(300 * time.Millisecond) // let the fsnotify watch become active
	if err := os.WriteFile(path, []byte("app: [broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	if got := mon.CurrentValue().Port; got != 1000 {
		t.Fatalf("expected old value kept after failed reload, got %d", got)
	}
	if len(reloadErrs) == 0 {
		t.Fatal("expected a WithOnReloadError callback")
	}
}

func TestLoadConfig(t *testing.T) {
	path := writeTemp(t, "app.yaml", "app:\n  port: 4321\n")
	cfg, err := kernel.LoadConfig[AppConfig]("app", config.WithFile(path), config.WithDefaults(func(c *AppConfig) { c.Host = "h" }))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 4321 || cfg.Host != "h" {
		t.Fatalf("unexpected LoadConfig result: %+v", cfg)
	}
}

// ---------- diagnostics / logger ----------

func TestDescribe(t *testing.T) {
	b := kernel.New()
	b.Provide[Logger](NewConsoleLogger).
		Provide[*Repository](NewRepository)

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	app.Describe(&sb)
	for _, want := range []string{"Logger", "Repository", "*kernel_test.Repository"} {
		if !strings.Contains(sb.String(), want) {
			t.Fatalf("Describe output missing %q:\n%s", want, sb.String())
		}
	}
}

func TestDot(t *testing.T) {
	b := kernel.New()
	b.Provide[Logger](NewConsoleLogger).
		Provide[*Repository](NewRepository)

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	app.Dot(&sb)
	if !strings.Contains(sb.String(), "digraph kernel") {
		t.Fatalf("unexpected DOT output:\n%s", sb.String())
	}
}

func TestLoggerAutoRegistered(t *testing.T) {
	b := kernel.New()
	b.Provide[Logger](NewConsoleLogger)

	app, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if app.Logger() == nil {
		t.Fatal("expected a default logger")
	}
	if _, err := app.Get[*slogLogger](); err != nil {
		t.Fatal("expected *slog.Logger to be auto-registered")
	}
}

type slogLogger = slog.Logger
