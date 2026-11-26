package config

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCORSOriginsFromEnvironment(t *testing.T) {
	// Load parses flags from os.Args; give it a clean set.
	oldArgs, oldFlags := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args, flag.CommandLine = oldArgs, oldFlags })
	flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)
	os.Args = []string{"test", "-config", filepath.Join("..", "..", "config", "config.yaml")}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"http://localhost:*", "http://127.0.0.1:*"}; !reflect.DeepEqual(cfg.HTTP.CORSAllowedOrigins, want) {
		t.Fatalf("default origins = %q, want %q", cfg.HTTP.CORSAllowedOrigins, want)
	}

	flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://your-project.site,https://www.your-project.site,http://localhost:*")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://your-project.site", "https://www.your-project.site", "http://localhost:*"}
	if !reflect.DeepEqual(cfg.HTTP.CORSAllowedOrigins, want) {
		t.Fatalf("origins from env = %q, want %q", cfg.HTTP.CORSAllowedOrigins, want)
	}
}
