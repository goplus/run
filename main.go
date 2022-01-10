package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/goplus/gop/x/gopproj"
	"github.com/goplus/gop/x/gopprojs"
)

var (
	flagAddr        string
	flagCacheDir    string
	flagAllowOrigin string
)

func init() {
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
	log.Println("url", r.URL)
	//w.Write([]byte(r.URL.Path))
	upath := r.URL.Path[1:]
	if strings.HasPrefix(upath, "github.com") {
		proj, err := h.Build(upath)
		if err != nil {
			fmt.Fprintf(w, "build error: %v", err)
			return
		}
		h.wasmProj = proj
		log.Println("build", proj, err)
	}
}

func fingToName(fing *gopproj.Fingerp) string {
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
	fileName := filepath.Join(h.cacheDir, pkgpath, fingToName(fp)+".wasm")
	wasmProj := &Project{PkgPath: pkgpath, Wasm: fileName, Dir: cmd.Dir}
	if _, err := os.Stat(fileName); err == nil {
		return wasmProj, nil
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "GOOS=js", "GOARCH=wasm")
	buildArgs := []string{"go", "build", "-o", fileName}
	cmd.Args = append(buildArgs, cmd.Args[2:]...)
	err = cmd.Run()
	return wasmProj, err
}
