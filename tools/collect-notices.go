// Preserve upstream license files of the modules actually imported by the binaries.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type module struct {
	Path, Version, Dir string
	Main               bool
}

func main() {
	output := flag.String("output", "/out/licenses/go-third-party-notices.txt", "notice destination")
	arch := flag.String("arch", runtime.GOARCH, "binary architecture")
	flag.Parse()
	command := exec.Command("go", "list", "-deps", "-json", "./cmd/proxy-worker", "./cmd/control-plane")
	command.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+*arch, "CGO_ENABLED=0")
	command.Stderr = os.Stderr
	data, err := command.Output()
	must(err)
	decoder := json.NewDecoder(bytes.NewReader(data))
	modules := map[string]module{}
	for {
		var pkg struct{ Module *module }
		err := decoder.Decode(&pkg)
		if err == io.EOF {
			break
		}
		must(err)
		if pkg.Module != nil && !pkg.Module.Main {
			modules[pkg.Module.Path] = *pkg.Module
		}
	}
	paths := make([]string, 0, len(modules))
	for path := range modules {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var out bytes.Buffer
	fmt.Fprintf(&out, "Third-party notices for RelayWeave Go binaries. Upstream terms do not license original RelayWeave source.\nCompiler: %s; target: linux/%s\n\n", runtime.Version(), *arch)
	for _, name := range []string{"LICENSE", "PATENTS", "AUTHORS"} {
		file := filepath.Join(runtime.GOROOT(), name)
		if _, err := os.Stat(file); err == nil {
			appendNotice(&out, "Go compiler/runtime "+name, file)
		}
	}
	for _, path := range paths {
		m := modules[path]
		if m.Dir == "" {
			panic("missing module source: " + path)
		}
		files := []string{}
		must(filepath.WalkDir(m.Dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := strings.ToUpper(entry.Name())
			for _, prefix := range []string{"LICENSE", "LICENCE", "NOTICE", "COPYING", "COPYRIGHT"} {
				if !entry.IsDir() && entry.Type().IsRegular() && (name == prefix || strings.HasPrefix(name, prefix+".") || strings.HasPrefix(name, prefix+"-")) {
					files = append(files, path)
					break
				}
			}
			return nil
		}))
		if len(files) == 0 {
			panic("missing upstream license files: " + m.Path + "@" + m.Version)
		}
		sort.Strings(files)
		for _, file := range files {
			relative, err := filepath.Rel(m.Dir, file)
			must(err)
			appendNotice(&out, m.Path+"@"+m.Version+"/"+relative, file)
		}
	}
	must(os.MkdirAll(filepath.Dir(*output), 0755))
	must(os.WriteFile(*output, out.Bytes(), 0644))
	fmt.Printf("Preserved upstream notices for %d imported Go modules\n", len(paths))
}

func appendNotice(out *bytes.Buffer, title, path string) {
	info, err := os.Stat(path)
	must(err)
	if info.Size() > 1<<20 {
		panic("unexpectedly large notice: " + title)
	}
	data, err := os.ReadFile(path)
	must(err)
	if bytes.IndexByte(data, 0) >= 0 {
		panic("nontext notice: " + title)
	}
	fmt.Fprintf(out, "--- %s ---\n%s\n\n", title, data)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
