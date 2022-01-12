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
	"sync"
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

var (
	spx_demo = "github.com/goplus/FlappyCalf"
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
	//http.Handle("/spx", http.FileServer(http.Dir(filepath.Join(dir, "spx"))))
	//http.Handle("/wasm", http.FileServer(http.Dir(filepath.Join(dir, "wasm"))))
	http.Handle("/", &Handler{cacheDir: dir})
	err := http.ListenAndServe(flagAddr, nil)
	if err != nil {
		log.Panicln(err)
	}
}

type Handler struct {
	cacheDir string
	projects sync.Map
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
		data := strings.ReplaceAll(indexHTML, "{{pkg}}", spx_demo)
		http.ServeContent(w, r, "", time.Now(), bytes.NewReader([]byte(data)))
		return
	}
	if strings.HasPrefix(upath, "github.com") {
		proj, err := h.Build(upath)
		log.Println("load pkg", upath, err)
		if err != nil {
			http.Error(w, fmt.Sprintf("load pkg %v error", upath), http.StatusInternalServerError)
			return
		}
		// create spx symlink
		fileName := filepath.Join(h.cacheDir, "spx", proj.PkgPath)
		dir, _ := filepath.Split(fileName)
		os.MkdirAll(dir, 0755)
		os.Remove(fileName)
		err = os.Symlink(proj.Dir, fileName)
		if err != nil {
			http.Error(w, fmt.Sprintf("link pkg %v error", upath), http.StatusInternalServerError)
		}
		h.projects.Store(upath, proj)
		http.Redirect(w, r, "/spx/"+upath+"/index.html", http.StatusSeeOther)
		return
	} else if strings.HasPrefix(upath, "spx/") {
		rpath := upath[4:]
		pos := strings.LastIndex(rpath, "/")
		pkg, file := rpath[:pos], rpath[pos+1:]
		switch file {
		case "index.html":
			value, ok := h.projects.Load(pkg)
			if ok {
				proj := value.(*Project)
				var argv []string
				argv = append(argv, `"`+template.JSEscapeString(proj.PkgPath)+`"`)
				data := strings.ReplaceAll(runHTML, "{{.Argv}}", "["+strings.Join(argv, ", ")+"]")
				data = strings.ReplaceAll(data, "{{main.wasm}}", "/wasm/"+proj.PkgPath+"/"+filepath.Base(proj.Wasm))
				http.ServeContent(w, r, "index.html", time.Now(), bytes.NewReader([]byte(data)))
				return
			}
		case "wasm_exec.js":
			_, ok := h.projects.Load(pkg)
			if ok {
				f := filepath.Join(runtime.GOROOT(), "misc", "wasm", "wasm_exec.js")
				serveFile(w, r, upath, f)
				return
			}
		case "_wait":
			waitForUpdate(w, r)
			return
		case "_notify":
			notifyWaiters(w, r)
			return
		}
		fileName := filepath.Clean(filepath.Join(h.cacheDir, upath))
		if !strings.HasPrefix(fileName, filepath.Join(h.cacheDir, "spx")) {
			http.Error(w, fmt.Sprintf("load %v error", upath), http.StatusInternalServerError)
			return
		}
		serveFile(w, r, upath, fileName)
		return
	} else if strings.HasPrefix(upath, "wasm/") {
		rpath := upath[5:]
		pos := strings.LastIndex(rpath, "/")
		pkg, _ := rpath[:pos], rpath[pos+1:]
		value, ok := h.projects.Load(pkg)
		if !ok {
			http.Error(w, fmt.Sprintf("run pkg %v error", pkg), http.StatusInternalServerError)
			return
		}
		proj := value.(*Project)
		serveFile(w, r, "main.wasm", proj.Wasm)
		return
	}
	http.Error(w, upath, http.StatusInternalServerError)
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
	fileName := filepath.Join(h.cacheDir, "wasm", pkgpath, fingerpToName(fp)+".wasm")
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
  const resp = await fetch('{{main.wasm}}');
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
