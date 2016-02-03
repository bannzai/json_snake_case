package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/format"
	"go/parser"
	"go/token"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

var tagRegex = regexp.MustCompile(`([0-9a-zA-Z,_=&\(\)]+)(:( )?"([0-9a-zA-Z,_=&\(\)]*)")?`)

var (
	typeNames = flag.String("type", "", "comma-separated list of type names; must be set")
	output    = flag.String("output", "", "output file name; default srcdir/<type>_json.go")
)

// Usage is a replacement usage function for the flags package.
func Usage() {
	fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
	flag.PrintDefaults()
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("json_snake: ")
	flag.Usage = Usage
	flag.Parse()
	if len(*typeNames) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	types := strings.Split(*typeNames, ",")
	// We accept either one directory or a list of files. Which do we have?
	args := flag.Args()
	if len(args) == 0 {
		// Default: process whole package in current directory.
		args = []string{"."}
	}

	g := &Generator{}
	g.pkg = &Package{}
	if len(args) == 1 && isDirectory(args[0]) {
		dir := args[0]
		p, err := build.Default.ImportDir(dir, 0)
		if err != nil {
			log.Fatalf("cannot process directory %s: %s", dir, err)
		}
		g.pkg.Dir = dir
		g.pkg.name = p.Name

		// TODO: support only gofile
		files := make([]File, len(p.GoFiles))
		for i, v := range p.GoFiles {
			files[i] = File{
				Name: prefixDirectory(g.pkg.Dir, v),
			}
		}
		g.pkg.Files = files

	} else {
		// TODO: supported files
		log.Fatalf("not supported files")
	}

	g.Printf("// Code generated by \"json_snake_case %s\"; DO NOT EDIT\n", strings.Join(os.Args[1:], " "))
	g.Printf("\n")
	g.Printf("package %s", g.pkg.name)
	g.Printf("\n")
	g.Printf("import \"encoding/json\"\n")

	fs := token.NewFileSet()
	for i, v := range g.pkg.Files {
		parsedFile, err := parser.ParseFile(fs, v.Name, nil, 0)
		if err != nil {
			log.Fatalf("parsing package: %s: %s", v.Name, err)
		}
		g.pkg.Files[i].AstFile = parsedFile
	}

	for _, v := range g.pkg.Files {
		for _, decl := range v.AstFile.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			if genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				name := typeSpec.Name.Name
				if !contains(types, name) {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				g.Printf("type %sJSON struct {", name)
				g.Printf("\n")
				fieldNames := make([]string, len(structType.Fields.List))
				for i, field := range structType.Fields.List {
					fieldName := field.Names[0].Name
					fieldNames[i] = fieldName

					identType, ok := field.Type.(*ast.Ident)
					if !ok {
						continue
					}
					fieldType := identType.Name
					tagValue := ""
					if field.Tag != nil {
						tagValue = field.Tag.Value[1 : len(field.Tag.Value)-1]
					}
					tags := tagParser(tagValue)
					jsonTag, ok := tags["json"]
					if ok {
						if strings.HasPrefix(jsonTag, ",") {
							tags["json"] = CamelToSnake(fieldName) + tags["json"]
						}
					} else {
						tags["json"] = CamelToSnake(fieldName)
					}

					g.Printf("%s %s `%s`", fieldName, fieldType, tagString(tags))
					g.Printf("\n")
				}
				g.Printf("}\n")

				g.Printf("\n")

				g.Printf("func (m %s) MarshalJSON() ([]byte, error) {\n", name)
				g.Printf("	j := New%sJSON(&m)\n", name)
				g.Printf("	return json.Marshal(j)\n")
				g.Printf("}\n")

				g.Printf("\n")

				g.Printf("func New%sJSON(m *%s) *%sJSON {\n", name, name, name)
				g.Printf("	v := &%sJSON{}\n", name)
				for _, fieldName := range fieldNames {
					g.Printf("	v.%s = m.%s\n", fieldName, fieldName)
				}
				g.Printf("return v\n")
				g.Printf("}\n")

				g.Printf("\n")
			}
		}
	}

	// Format the output.
	src := g.format()

	// Write to file.
	outputName := *output
	if outputName == "" {
		baseName := fmt.Sprintf("%s_json.go", types[0])
		outputName = filepath.Join(g.pkg.Dir, strings.ToLower(baseName))
	}
	err := ioutil.WriteFile(outputName, src, 0644)
	if err != nil {
		log.Fatalf("writing output: %s", err)
	}
}

type Generator struct {
	buf bytes.Buffer
	pkg *Package
}

func (g *Generator) Printf(format string, args ...interface{}) {
	fmt.Fprintf(&g.buf, format, args...)
}

// format returns the gofmt-ed contents of the Generator's buffer.
func (g *Generator) format() []byte {
	src, err := format.Source(g.buf.Bytes())
	if err != nil {
		// Should never happen, but can arise when developing this code.
		// The user can compile the output to see the error.
		log.Printf("warning: internal error: invalid Go generated: %s", err)
		log.Printf("warning: compile the package to analyze the error")
		return g.buf.Bytes()
	}
	return src
}

type Package struct {
	Dir   string
	name  string
	Files []File
}

type File struct {
	Name    string
	AstFile *ast.File
}

// isDirectory reports whether the named file is a directory.
func isDirectory(name string) bool {
	info, err := os.Stat(name)
	if err != nil {
		log.Fatal(err)
	}
	return info.IsDir()
}

func prefixDirectory(directory string, name string) string {
	if directory == "." {
		return name
	}
	return filepath.Join(directory, name)
}

// utils

func contains(list []string, key string) bool {
	for _, v := range list {
		if v == key {
			return true
		}
	}
	return false
}

func tagParser(input string) map[string]string {
	tags := make(map[string]string)
	list := tagRegex.FindAllStringSubmatch(input, -1)
	for _, v := range list {
		tags[v[1]] = v[4]
	}
	return tags
}

func tagString(tags map[string]string) string {
	output := ""
	for i, v := range tags {
		if v == "" {
			output = fmt.Sprintf("%s %s", output, i)
			continue
		}
		output = fmt.Sprintf(`%s %s:"%s"`, output, i, v)
	}
	return strings.TrimPrefix(output, " ")
}

func CamelToSnake(s string) string {
	var result string
	var words []string
	var lastPos int
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if i > 0 && unicode.IsUpper(rs[i]) {
			if initialism := startsWithInitialism(s[lastPos:]); initialism != "" {
				words = append(words, initialism)

				i += len(initialism) - 1
				lastPos = i
				continue
			}
			words = append(words, s[lastPos:i])
			lastPos = i
		}
	}
	if s[lastPos:] != "" {
		words = append(words, s[lastPos:])
	}
	for k, word := range words {
		if k > 0 {
			result += "_"
		}
		result += strings.ToLower(word)
	}
	return result
}

// startsWithInitialism returns the initialism if the given string begins with it
func startsWithInitialism(s string) string {
	var initialism string
	// the longest initialism is 5 char, the shortest 2
	for i := 1; i <= 5; i++ {
		if len(s) > i-1 && commonInitialisms[s[:i]] {
			initialism = s[:i]
		}
	}
	return initialism
}

// copy from https://github.com/golang/lint
var commonInitialisms = map[string]bool{
	"API":   true,
	"ASCII": true,
	"CPU":   true,
	"CSS":   true,
	"DNS":   true,
	"EOF":   true,
	"GUID":  true,
	"HTML":  true,
	"HTTP":  true,
	"HTTPS": true,
	"ID":    true,
	"IP":    true,
	"JSON":  true,
	"LHS":   true,
	"QPS":   true,
	"RAM":   true,
	"RHS":   true,
	"RPC":   true,
	"SLA":   true,
	"SMTP":  true,
	"SQL":   true,
	"SSH":   true,
	"TCP":   true,
	"TLS":   true,
	"TTL":   true,
	"UDP":   true,
	"UI":    true,
	"UID":   true,
	"UUID":  true,
	"URI":   true,
	"URL":   true,
	"UTF8":  true,
	"VM":    true,
	"XML":   true,
	"XSRF":  true,
	"XSS":   true,
}
