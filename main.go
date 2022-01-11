package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"

	"github.com/goplus/gop/x/gopproj"
	"github.com/goplus/gop/x/gopprojs"
)

var (
	flagAddr        string
	flagCacheDir    string
	flagAllowOrigin string
	flagVerbose     bool
)

func init() {
	flag.BoolVar(&flagVerbose, "v", false, "print verbose information")
	flag.StringVar(&flagAddr, "http", ":8080", "HTTP bind address to serve")
	flag.StringVar(&flagCacheDir, "cache", "cache", "wasm cache dir")
	flag.StringVar(&flagAllowOrigin, "allow-origin", "", "Allow specified origin (or * for all origins) to make requests to this server")
}

func main() {
	flag.Parse()
	dir := flagCacheDir
	if !filepath.IsAbs(dir) {
		wd, err := os.Getwd()
		if err != nil {
			log.Panicln(err)
		}
		dir = filepath.Join(wd, dir)
	}
	log.Println("start server", flagAddr)
	http.Handle("/", &Handler{cacheDir: dir})
	err := http.ListenAndServe(flagAddr, nil)
	if err != nil {
		log.Panicln(err)
	}
}

type Handler struct {
	cacheDir string
	wasmProj *Project
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if flagAllowOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", flagAllowOrigin)
	}
	if flagVerbose {
		log.Println("handle url", r.URL)
	}
	upath := r.URL.Path[1:]
	if upath == "" || upath == "index.html" {
		var data string
		if h.wasmProj != nil {
			data = strings.ReplaceAll(indexHTML, "{{pkg}}", h.wasmProj.PkgPath)
		} else {
			data = strings.ReplaceAll(indexHTML, "{{pkg}}", "github.com/goplus/FlappyCalf")
		}
		http.ServeContent(w, r, "", time.Now(), bytes.NewReader([]byte(data)))
		return
	}
	if strings.HasPrefix(upath, "github.com") {
		proj, err := h.Build(upath)
		log.Println("load pkg", upath, err)
		if err != nil {
			http.Error(w, fmt.Sprintf("load remote pkg %v error", upath), http.StatusInternalServerError)
			return
		}
		h.wasmProj = proj
		http.Redirect(w, r, "/run.html", http.StatusSeeOther)
		return
	}
	if h.wasmProj == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	switch upath {
	case "run.html":
		var argv []string
		argv = append(argv, `"`+template.JSEscapeString(h.wasmProj.PkgPath)+`"`)
		data := strings.ReplaceAll(runHTML, "{{.Argv}}", "["+strings.Join(argv, ", ")+"]")
		http.ServeContent(w, r, "run.html", time.Now(), bytes.NewReader([]byte(data)))
	case "wasm_exec.js":
		f := filepath.Join(runtime.GOROOT(), "misc", "wasm", "wasm_exec.js")
		serveFile(w, r, upath, f)
		return
	case "main.wasm":
		serveFile(w, r, upath, h.wasmProj.Wasm)
		return
	case "_wait":
		waitForUpdate(w, r)
		return
	case "_notify":
		notifyWaiters(w, r)
		return
	default:
		//assets/sprites/Logo/index.json
		fileName := filepath.Clean(filepath.Join(h.wasmProj.Dir, upath))
		if !strings.HasPrefix(fileName, h.wasmProj.Dir) {
			http.Error(w, fmt.Sprintf("load %v error", upath), http.StatusInternalServerError)
			return
		}
		serveFile(w, r, upath, fileName)
		return
	}
}

func serveFile(w http.ResponseWriter, r *http.Request, upath string, fileName string) {
	f, err := os.Open(fileName)
	if err != nil {
		http.Error(w, fmt.Sprintf("load %v error", upath), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	d, err := f.Stat()
	if err != nil {
		http.Error(w, fmt.Sprintf("load %v error", upath), http.StatusInternalServerError)
	}
	http.ServeContent(w, r, upath, d.ModTime(), f)
}

var (
	waitChannel = make(chan struct{})
)

func waitForUpdate(w http.ResponseWriter, r *http.Request) {
	waitChannel <- struct{}{}
	http.ServeContent(w, r, "", time.Now(), bytes.NewReader(nil))
}

func notifyWaiters(w http.ResponseWriter, r *http.Request) {
	for {
		select {
		case <-waitChannel:
		default:
			http.ServeContent(w, r, "", time.Now(), bytes.NewReader(nil))
			return
		}
	}
}

func fingerpToName(fing *gopproj.Fingerp) string {
	return fmt.Sprintf("%x-%v", fing.Hash, fing.ModTime.Unix())
}

func cleanPkg(pkgpath string) string {
	if pos := strings.Index(pkgpath, "@"); pos != -1 {
		return pkgpath[:pos]
	}
	return pkgpath
}

type Project struct {
	PkgPath string
	Wasm    string
	Dir     string
}

func (h *Handler) Build(pkgpath string) (*Project, error) {
	proj, args, err := gopprojs.ParseOne(pkgpath)
	if err != nil {
		return nil, fmt.Errorf("parser pkg failed: %v", err)
	}
	flags := 0
	ctx, goProj, err := gopproj.OpenProject(flags, proj)
	if err != nil {
		return nil, fmt.Errorf("open project failed: %v", err)
	}
	goProj.ExecArgs = args
	if goProj.FlagRTOE {
		goProj.UseDefaultCtx = true
	}
	fp, err := goProj.Fingerp()
	if err != nil {
		return nil, err
	}
	cmd := ctx.GoCommand("run", goProj)
	fileName := filepath.Join(h.cacheDir, pkgpath, fingerpToName(fp)+".wasm")
	wasmProj := &Project{PkgPath: pkgpath, Wasm: fileName, Dir: cmd.Dir}
	if _, err := os.Stat(fileName); err == nil {
		return wasmProj, nil
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "GOOS=js", "GOARCH=wasm")
	buildArgs := []string{"go", "build", "-tags", "canvas", "-o", fileName}
	cmd.Args = append(buildArgs, cmd.Args[2:]...)
	err = cmd.Run()
	return wasmProj, err
}

const indexHTML = `
<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
</head>
<body>
run spx game:
<input style="width:50%" id="spxurl" name="spxurl" type="text" value="{{pkg}}"/>
<input type="button" value="Run" onclick="run();" />
<script>
function run(){
    var val=document.getElementById("spxurl").value;
    location.href = "/"+val;
}
</script>
</body>
</html>
`

const runHTML = `<!DOCTYPE html>
<!-- Polyfill for the old Edge browser -->
<script src="https://cdn.jsdelivr.net/npm/text-encoding@0.7.0/lib/encoding.min.js"></script>
<script src="wasm_exec.js"></script>
<script>
(async () => {
  const resp = await fetch('main.wasm');
  if (!resp.ok) {
    const pre = document.createElement('pre');
    pre.innerText = await resp.text();
    document.body.appendChild(pre);
  } else {
    const src = await resp.arrayBuffer();
    const go = new Go();
    const result = await WebAssembly.instantiate(src, go.importObject);
    go.argv = {{.Argv}};
    go.run(result.instance);
  }
  const reload = await fetch('_wait');
  // The server sends a response for '_wait' when a request is sent to '_notify'.
  if (reload.ok) {
    location.reload();
  }
})();
</script>
`
