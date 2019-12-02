// SPDX-License-Identifier: Unlicense OR MIT

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/parser"
	"gopkg.in/yaml.v2"
)

type frontMatter struct {
	Title string `yaml:"title"`
}

type page struct {
	Front   frontMatter
	Content []byte
}

var (
	docTmpl   *template.Template
	pages     = make(map[string][]byte)
	errNoPage = errors.New("no such page")
)

const (
	contentRoot = "content"
	includeRoot = "include"
)

func init() {
	docTmpl = template.Must(template.ParseFiles(
		filepath.Join("template", "page.tmpl"),
		filepath.Join("template", "root.tmpl"),
	))
	if err := loadDocs(filepath.Join(contentRoot)); err != nil {
		log.Fatal(err)
	}
}

func loadDocs(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		name := path[len(root):]
		name = name[:len(name)-len(".md")]
		content, err := loadMarkdown(name)
		if err != nil {
			return err
		}
		pages[name] = content
		return nil
	})
}

func servePage(w io.Writer, path string) error {
	var page []byte
	if os.Getenv("GAE_APPLICATION") != "" {
		p, ok := pages[path]
		if !ok {
			return errNoPage
		}
		page = p
	} else {
		p, err := loadMarkdown(path)
		if err != nil {
			return err
		}
		page = p
	}
	_, err := w.Write(page)
	return err
}

func loadMarkdown(url string) ([]byte, error) {
	path := filepath.Join(contentRoot, url+".md")
	content, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	page, err := loadPage(content)
	if err != nil {
		return nil, fmt.Errorf("%s: failed to parse front matter: %v", path, err)
	}
	if page.Front.Title == "" {
		page.Front.Title = "Gio - immediate mode GUI in Go"
	}
	mdp := parser.NewWithExtensions(parser.CommonExtensions | parser.Includes | parser.Attributes)
	mdp.Opts.ReadIncludeFn = func(from, path string, addr []byte) []byte {
		content, err := includeExample(path, string(addr))
		if err != nil {
			content = []byte(err.Error())
		}
		return content
	}
	html := markdown.ToHTML(page.Content, mdp, nil)
	args := struct {
		Front   frontMatter
		Content template.HTML
	}{page.Front, template.HTML(html)}
	var buf bytes.Buffer
	if err := docTmpl.ExecuteTemplate(&buf, "root", args); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func includeExample(path string, addr string) ([]byte, error) {
	path = filepath.Join(includeRoot, path)
	content, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if addr != "" {
		rng := strings.SplitN(addr, ",", 2)
		if len(rng) != 2 {
			return nil, fmt.Errorf("invalid address: %s", addr)
		}
		start, end := rng[0], rng[1]
		startR, err := regexFor(start)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", start, err)
		}
		endR, err := regexFor(end)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", end, err)
		}
		startIdx := startR.FindIndex(content)
		if startIdx != nil {
			content = content[startIdx[0]:]
		}
		endIdx := endR.FindIndex(content)
		if endIdx != nil {
			content = content[:endIdx[1]]
		}
		_ = endR
	}
	content = bytes.Trim(content, "\n\r")
	content = append(content, '\n')
	return undent(content), err
}

// undent removes the number of leading tab characters in the first
// line from all lines.
func undent(text []byte) []byte {
	first := true
	ntabs := 0
	var buf bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(text))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "OMIT") {
			continue
		}
		if first {
			for ntabs < len(line) && line[ntabs] == '\t' {
				ntabs++
			}
			first = false
		}
		i := 0
		for i < ntabs && len(line) > 0 && line[0] == '\t' {
			i++
			line = line[1:]
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func regexFor(r string) (*regexp.Regexp, error) {
	if len(r) < 2 || r[0] != '/' || r[len(r)-1] != '/' {
		return nil, errors.New("missing / separators")
	}
	r = r[1 : len(r)-1]
	return regexp.Compile(r)
}

func loadPage(content []byte) (page, error) {
	front := frontMatter{}
	split := bytes.SplitN(content, []byte("---"), 3)
	if len(split) < 3 || len(split[0]) > 0 {
		// No front matter available.
		return page{front, content}, nil
	}
	header, md := split[1], split[2]
	if err := yaml.Unmarshal(header, &front); err != nil {
		return page{}, err
	}
	return page{front, md}, nil
}

// pageHandler serves a page from the content directory, or
// falls back to a fallback handler if none were found.
func pageHandler(fallback http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasSuffix(path, "/") {
			path = path + "index"
		}
		if err := servePage(w, path); err != nil {
			if err == errNoPage {
				fallback.ServeHTTP(w, r)
			} else {
				log.Printf("%s: %v", path, err)
				http.Error(w, "failed to serve page", http.StatusInternalServerError)
			}
		}
	})
}
