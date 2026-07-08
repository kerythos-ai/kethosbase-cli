package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kerythos-ai/kethosbase-cli/internal/api"
	"github.com/kerythos-ai/kethosbase-cli/internal/config"
	"github.com/kerythos-ai/kethosbase-cli/internal/functions/bundle"
	"github.com/kerythos-ai/kethosbase-cli/internal/functions/javytool"
	"github.com/kerythos-ai/kethosbase-cli/internal/functions/wasmcheck"
	"github.com/spf13/cobra"
)

// maxFunctionBytes mirrors the platform's 8 MiB deploy cap so we fail locally
// with a clear message instead of a 413 after a wasted upload.
const maxFunctionBytes = 8 << 20

// funcNameRe mirrors the server's function-name rule.
var funcNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func newFunctionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "functions",
		Short: "Author and deploy Edge Functions",
	}
	cmd.AddCommand(newFunctionsDeployCmd())
	return cmd
}

func newFunctionsDeployCmd() *cobra.Command {
	var name, ref, apiURL, out string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "deploy <file.ts>",
		Short: "Bundle a TypeScript/JavaScript function, compile it to Wasm, and deploy it",
		Long: "Bundles <file.ts> together with the @kethosbase/functions SDK into a single\n" +
			"JavaScript file, compiles it to a WebAssembly module with Javy (QuickJS on\n" +
			"Wasm), validates the module's imports, and uploads it via the management API.\n\n" +
			"The function name defaults to the file's base name; override with --name.\n" +
			"Use --dry-run to produce the .wasm without uploading (implies --out).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entry := args[0]

			fnName := name
			if fnName == "" {
				fnName = defaultName(entry)
			}
			if !funcNameRe.MatchString(fnName) {
				return fmt.Errorf("invalid function name %q: must match ^[a-z0-9][a-z0-9_-]{0,63}$ (pass --name)", fnName)
			}

			// 1. Bundle TS + SDK -> single JS.
			fmt.Fprintf(os.Stderr, "Bundling %s...\n", entry)
			b, err := bundle.Bundle(entry)
			if err != nil {
				return err
			}

			// 2. Compile JS -> module.wasm via Javy + the vendored plugin.
			fmt.Fprintln(os.Stderr, "Compiling to WebAssembly (Javy)...")
			tool, err := javytool.Ensure()
			if err != nil {
				return fmt.Errorf("prepare Javy toolchain: %w", err)
			}
			tmpJS, err := os.CreateTemp("", "kb-fn-*.js")
			if err != nil {
				return err
			}
			defer os.Remove(tmpJS.Name())
			if _, err := tmpJS.Write(b.JS); err != nil {
				tmpJS.Close()
				return err
			}
			tmpJS.Close()

			wasmPath := out
			if wasmPath == "" {
				tmpWasm, err := os.CreateTemp("", "kb-fn-*.wasm")
				if err != nil {
					return err
				}
				tmpWasm.Close()
				wasmPath = tmpWasm.Name()
				if !dryRun {
					defer os.Remove(wasmPath)
				}
			}
			if err := tool.Build(tmpJS.Name(), wasmPath); err != nil {
				return err
			}

			wasm, err := os.ReadFile(wasmPath)
			if err != nil {
				return err
			}

			// 3. Validate imports (only kethosbase + wasi) and _start export,
			// matching the platform's deploy-time validator, plus the size cap.
			info, err := wasmcheck.Validate(wasm)
			if err != nil {
				return fmt.Errorf("produced module failed validation: %w", err)
			}
			if len(wasm) > maxFunctionBytes {
				return fmt.Errorf("module is %d bytes, over the %d byte limit", len(wasm), maxFunctionBytes)
			}
			fmt.Fprintf(os.Stderr, "Built %s (%d bytes; imports: %s).\n",
				fnName, len(wasm), strings.Join(info.ImportModules, ", "))

			if dryRun {
				if out == "" {
					return fmt.Errorf("--dry-run needs --out <file.wasm> to keep the module")
				}
				fmt.Fprintf(os.Stderr, "Dry run: wrote %s (not uploaded).\n", wasmPath)
				return nil
			}

			// 4. Upload via the management API.
			ctx := context.Background()
			creds, err := config.LoadCredentials()
			if err != nil {
				return err
			}
			if creds.AccessToken == "" {
				return fmt.Errorf("not logged in: run `kethosbase login` first")
			}
			projectRef, err := resolveRef(ref)
			if err != nil {
				return err
			}
			client := api.New(apiURL, creds.AccessToken)
			fmt.Fprintf(os.Stderr, "Deploying %s to project %s...\n", fnName, projectRef)
			res, err := client.DeployFunction(ctx, projectRef, fnName, wasm)
			if err != nil {
				return err
			}
			fmt.Printf("Deployed function %s (sha256 %s, %d bytes).\n", res.Name, short(res.SHA256), res.Size)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "function name (default: the file's base name)")
	cmd.Flags().StringVar(&ref, "project", "", "project ref (default: the linked project in kethosbase.json)")
	cmd.Flags().StringVar(&apiURL, "api", "", "control-plane API base URL (default "+api.DefaultBaseURL+")")
	cmd.Flags().StringVarP(&out, "out", "o", "", "write the compiled .wasm to this path")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "build and validate the .wasm but do not upload (requires --out)")
	return cmd
}

// resolveRef picks the project ref from --project or the linked kethosbase.json.
func resolveRef(refFlag string) (string, error) {
	if refFlag != "" {
		return refFlag, nil
	}
	proj, err := config.LoadProject(".")
	if err != nil {
		return "", err
	}
	if proj == nil || proj.Ref == "" {
		return "", fmt.Errorf("no project: pass --project <ref> or run `kethosbase link` first")
	}
	return proj.Ref, nil
}

// defaultName derives a function name from a file path: base name without
// extension, lower-cased, with unsupported characters replaced by '-'.
func defaultName(path string) string {
	base := filepath.Base(path)
	if i := strings.LastIndex(base, "."); i > 0 {
		base = base[:i]
	}
	base = strings.ToLower(base)
	var b strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
