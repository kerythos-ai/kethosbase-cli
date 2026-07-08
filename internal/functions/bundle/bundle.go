// Package bundle turns a customer's TypeScript/JavaScript Function entrypoint
// into a single self-contained JS file suitable for Javy compilation.
//
// It uses esbuild as a Go library (no separate binary) to bundle the entrypoint
// and its imports. The `@kethosbase/functions` SDK import resolves to the real
// package if it is installed in node_modules; otherwise it falls back to an
// embedded shim implementing the shared bridge contract against the plugin's
// __kb_* globals. The output is an IIFE (classic script) because Javy/QuickJS
// evaluates the module as a script and runs its top-level code — which is where
// the SDK's serve() reads the request and writes the response.
package bundle

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

//go:embed sdk_shim.js
var sdkShim string

// sdkModule is the bare specifier customers import.
const sdkModule = "@kethosbase/functions"

// Result is the bundled output.
type Result struct {
	JS []byte
}

// Bundle bundles entrypoint (a .ts/.js/.mjs file) into a single JS script.
// resolveDir is used to locate node_modules for a real @kethosbase/functions;
// pass the entrypoint's directory (or "" to use its dir).
func Bundle(entrypoint string) (*Result, error) {
	abs, err := filepath.Abs(entrypoint)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("read %s: %w", entrypoint, err)
	}

	result := api.Build(api.BuildOptions{
		EntryPoints:   []string{abs},
		Bundle:        true,
		Format:        api.FormatIIFE,
		Platform:      api.PlatformNeutral,
		Target:        api.ES2020,
		LogLevel:      api.LogLevelSilent,
		Write:         false,
		Sourcemap:     api.SourceMapNone,
		AbsWorkingDir: filepath.Dir(abs),
		// Javy has no Node/browser runtime; keep output minimal and self-contained.
		Plugins: []api.Plugin{sdkShimPlugin()},
	})

	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("bundle failed: %s", formatMessages(result.Errors))
	}
	if len(result.OutputFiles) == 0 {
		return nil, fmt.Errorf("bundle produced no output")
	}
	return &Result{JS: result.OutputFiles[0].Contents}, nil
}

// sdkShimPlugin resolves the @kethosbase/functions bare import to the embedded
// shim ONLY when it is not resolvable from node_modules, so a real installed
// SDK always wins.
func sdkShimPlugin() api.Plugin {
	return api.Plugin{
		Name: "kethosbase-sdk-shim",
		Setup: func(build api.PluginBuild) {
			build.OnResolve(api.OnResolveOptions{Filter: `^@kethosbase/functions$`},
				func(args api.OnResolveArgs) (api.OnResolveResult, error) {
					if p := findInstalledSDK(args.ResolveDir); p != "" {
						// Let esbuild resolve the real package normally.
						return api.OnResolveResult{}, nil
					}
					return api.OnResolveResult{
						Path:      sdkModule,
						Namespace: "kethosbase-shim",
					}, nil
				})
			build.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "kethosbase-shim"},
				func(args api.OnLoadArgs) (api.OnLoadResult, error) {
					contents := sdkShim
					return api.OnLoadResult{
						Contents: &contents,
						Loader:   api.LoaderJS,
					}, nil
				})
		},
	}
}

// findInstalledSDK walks up from dir looking for
// node_modules/@kethosbase/functions; returns its path or "".
func findInstalledSDK(dir string) string {
	for {
		cand := filepath.Join(dir, "node_modules", "@kethosbase", "functions")
		if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
			return cand
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func formatMessages(msgs []api.Message) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteString("; ")
		}
		if m.Location != nil {
			fmt.Fprintf(&b, "%s:%d: ", m.Location.File, m.Location.Line)
		}
		b.WriteString(m.Text)
	}
	return b.String()
}
