package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	DefaultOutput = "./openapi.yml"
)

type Request struct {
	Method     string
	URL        string
	Headers    map[string]string
	Query      map[string]string
	PathParams map[string]string
	Body       string
	BodyType   string
	Name       string
	Tag        string
}

type OpenAPI struct {
	OpenAPI string                          `yaml:"openapi"`
	Info    Info                            `yaml:"info"`
	Servers []Server                        `yaml:"servers,omitempty"`
	Paths   map[string]map[string]Operation `yaml:"paths"`
}

type Info struct {
	Title   string `yaml:"title"`
	Version string `yaml:"version"`
}

type Server struct {
	URL string `yaml:"url"`
}

type Operation struct {
	Summary     string              `yaml:"summary,omitempty"`
	Tags        []string            `yaml:"tags,omitempty"`
	Parameters  []Parameter         `yaml:"parameters,omitempty"`
	RequestBody *RequestBody        `yaml:"requestBody,omitempty"`
	Responses   map[string]Response `yaml:"responses"`
}

type Parameter struct {
	Name     string `yaml:"name"`
	In       string `yaml:"in"`
	Required bool   `yaml:"required"`
	Schema   Schema `yaml:"schema"`
	Example  any    `yaml:"example,omitempty"`
}

type Schema struct {
	Type string `yaml:"type,omitempty"`
}

type RequestBody struct {
	Required bool                 `yaml:"required"`
	Content  map[string]MediaType `yaml:"content"`
}

type MediaType struct {
	Schema  *MediaSchema `yaml:"schema,omitempty"`
	Example any          `yaml:"example,omitempty"`
}

type MediaSchema struct {
	Type string `yaml:"type"`
}

type Response struct {
	Description string `yaml:"description"`
}

var sectionRegex = regexp.MustCompile(`^([\w-]+)(?::([\w-]+))?\s*\{$`)
var pathParamRegex = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

func parseBru(content string) Request {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	result := Request{
		Method:     "get",
		Headers:    map[string]string{},
		Query:      map[string]string{},
		PathParams: map[string]string{},
		Name:       "Unnamed",
	}

	section := ""
	sectionType := ""
	buffer := []string{}
	bodyDepth := 0

	isMethodBlock := func(name string) bool {
		switch name {
		case "get", "post", "put", "patch", "delete", "options", "head":
			return true
		default:
			return false
		}
	}

	flushBuffer := func() {
		if section == "body" && len(buffer) > 0 {
			raw := strings.TrimSpace(strings.Join(buffer, "\n"))
			if raw != "" {
				result.Body = raw
			}
		}
		buffer = []string{}
	}

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		if match := sectionRegex.FindStringSubmatch(line); match != nil {
			flushBuffer()
			name := strings.ToLower(match[1])
			typeName := ""
			if len(match) > 2 {
				typeName = strings.ToLower(match[2])
			}

			if isMethodBlock(name) {
				section = "method"
				sectionType = name
				result.Method = name
			} else if name == "meta" {
				section = "meta"
				sectionType = ""
			} else if name == "headers" {
				section = "headers"
				sectionType = ""
			} else if name == "query" {
				section = "query"
				sectionType = ""
			} else if name == "params" {
				if typeName == "query" {
					section = "params_query"
				} else {
					section = "params"
				}
				sectionType = typeName
			} else if name == "body" {
				section = "body"
				sectionType = typeName
				result.BodyType = typeName
				bodyDepth = 1
			} else {
				section = "ignore"
				sectionType = ""
			}
			_ = sectionType
			continue
		}

		if section == "body" {
			// Count all braces in the line to track nesting depth
			for _, ch := range rawLine {
				if ch == '{' {
					bodyDepth++
				} else if ch == '}' {
					bodyDepth--
				}
			}
			if bodyDepth <= 0 {
				flushBuffer()
				section = ""
				sectionType = ""
				bodyDepth = 0
				continue
			}
			buffer = append(buffer, rawLine)
			continue
		}

		if line == "}" {
			flushBuffer()
			section = ""
			sectionType = ""
			bodyDepth = 0
			continue
		}

		switch section {
		case "meta":
			k, v := splitKeyValue(line)
			if k == "name" {
				result.Name = v
			} else if k == "method" {
				result.Method = strings.ToLower(v)
			} else if k == "url" {
				setURL(&result, v)
			}
		case "method":
			k, v := splitKeyValue(line)
			if k == "url" {
				setURL(&result, v)
			}
		case "headers":
			k, v := splitKeyValue(line)
			if k != "" {
				result.Headers[k] = v
			}
		case "query":
			k, v := splitKeyValue(line)
			if k != "" {
				result.Query[k] = v
			}
		case "params_query":
			k, v := splitKeyValue(line)
			if k != "" {
				result.Query[k] = v
			}
		case "params":
			k, v := splitKeyValue(line)
			if k != "" {
				result.PathParams[k] = v
			}
		}
	}

	flushBuffer()
	return result
}

func splitKeyValue(line string) (string, string) {
	parts := strings.Split(line, ":")
	if len(parts) == 0 {
		return "", ""
	}
	key := strings.TrimSpace(parts[0])
	value := strings.TrimSpace(strings.Join(parts[1:], ":"))
	return key, value
}

func setURL(req *Request, raw string) {
	cleaned, query := extractQueryFromURL(raw)
	req.URL = cleaned
	for k, v := range query {
		if _, exists := req.Query[k]; !exists {
			req.Query[k] = v
		}
	}
}

func extractQueryFromURL(raw string) (string, map[string]string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return raw, map[string]string{}
	}
	parts := strings.SplitN(trimmed, "?", 2)
	if len(parts) < 2 {
		return raw, map[string]string{}
	}
	query := map[string]string{}
	values, err := url.ParseQuery(parts[1])
	if err != nil {
		return parts[0], query
	}
	for k, v := range values {
		if len(v) > 0 {
			query[k] = v[0]
		} else {
			query[k] = ""
		}
	}
	return parts[0], query
}

func buildOpenAPI(requests []Request) OpenAPI {
	paths := map[string]map[string]Operation{}
	serverSet := map[string]bool{}

	for _, req := range requests {
		pathName, server := splitURL(req.URL)
		normalizedPath := normalizePathParams(pathName)

		if server != "" {
			serverSet[server] = true
		}
		if _, ok := paths[normalizedPath]; !ok {
			paths[normalizedPath] = map[string]Operation{}
		}

		parameters := []Parameter{}
		for name, value := range req.Query {
			parameters = append(parameters, Parameter{
				Name:     name,
				In:       "query",
				Required: false,
				Schema:   Schema{Type: "string"},
				Example:  value,
			})
		}
		for name, value := range req.PathParams {
			parameters = append(parameters, Parameter{
				Name:     name,
				In:       "path",
				Required: true,
				Schema:   Schema{Type: "string"},
				Example:  value,
			})
		}

		for _, name := range extractPathParams(normalizedPath) {
			if !hasPathParam(parameters, name) {
				parameters = append(parameters, Parameter{
					Name:     name,
					In:       "path",
					Required: true,
					Schema:   Schema{Type: "string"},
				})
			}
		}

		op := Operation{
			Summary:   req.Name,
			Responses: map[string]Response{"200": {Description: "Success"}},
		}
		if req.Tag != "" {
			op.Tags = []string{req.Tag}
		}
		if len(parameters) > 0 {
			op.Parameters = parameters
		}
		if rb := buildRequestBody(req); rb != nil {
			op.RequestBody = rb
		}

		paths[normalizedPath][req.Method] = op
	}

	servers := []Server{}
	for url := range serverSet {
		servers = append(servers, Server{URL: url})
	}

	openapi := OpenAPI{
		OpenAPI: "3.0.0",
		Info: Info{
			Title:   "API from Bruno",
			Version: "1.0.0",
		},
		Paths: paths,
	}
	if len(servers) > 0 {
		openapi.Servers = servers
	}
	return openapi
}

func hasPathParam(params []Parameter, name string) bool {
	for _, p := range params {
		if p.In == "path" && p.Name == name {
			return true
		}
	}
	return false
}

func safeJSON(text string) any {
	var out any
	if err := json.Unmarshal([]byte(text), &out); err == nil {
		return out
	}
	return text
}

func collectBruFiles(dir string) ([]string, error) {
	results := []string{}
	walkFn := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".bru") {
			results = append(results, path)
		}
		return nil
	}
	if err := filepath.WalkDir(dir, walkFn); err != nil {
		return nil, err
	}
	return results, nil
}

func splitURL(raw string) (string, string) {
	if strings.TrimSpace(raw) == "" {
		return "/", ""
	}
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{{") && strings.Contains(trimmed, "}}") {
		endIdx := strings.Index(trimmed, "}}")
		base := trimmed[:endIdx+2]
		rest := trimmed[endIdx+2:]
		if rest == "" {
			rest = "/"
		}
		return rest, base
	}

	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		if u, err := url.Parse(trimmed); err == nil {
			pathName := u.Path
			if pathName == "" {
				pathName = "/"
			}
			return pathName, u.Scheme + "://" + u.Host
		}
		return "/", ""
	}

	if strings.HasPrefix(trimmed, "/") {
		return trimmed, ""
	}
	return "/" + trimmed, ""
}

func normalizePathParams(pathName string) string {
	re := regexp.MustCompile(`:([A-Za-z0-9_]+)`)
	return re.ReplaceAllString(pathName, "{$1}")
}

func extractPathParams(pathName string) []string {
	matches := pathParamRegex.FindAllStringSubmatch(pathName, -1)
	out := []string{}
	for _, m := range matches {
		if len(m) > 1 {
			out = append(out, m[1])
		}
	}
	return out
}

func buildRequestBody(req Request) *RequestBody {
	if strings.TrimSpace(req.Body) == "" {
		return nil
	}

	contentType := "application/json"
	if req.BodyType == "text" {
		contentType = "text/plain"
	}
	if req.BodyType == "graphql" {
		contentType = "application/graphql"
	}
	if v, ok := req.Headers["Content-Type"]; ok {
		contentType = v
	}
	if v, ok := req.Headers["content-type"]; ok {
		contentType = v
	}

	var media MediaType
	if strings.Contains(strings.ToLower(contentType), "json") {
		parsed := safeJSON(req.Body)
		media = MediaType{
			Schema:  &MediaSchema{Type: "object"},
			Example: parsed,
		}
	} else {
		media = MediaType{
			Schema:  &MediaSchema{Type: "string"},
			Example: req.Body,
		}
	}

	return &RequestBody{
		Required: true,
		Content: map[string]MediaType{
			contentType: media,
		},
	}
}

func main() {
	inputDir := flag.String("i", "", "Path ke folder Bruno collection")
	outputFile := flag.String("o", DefaultOutput, "Path output OpenAPI YAML")
	flag.Parse()

	if strings.TrimSpace(*inputDir) == "" {
		fmt.Println("Error: input directory wajib diisi dengan -i <path>")
		os.Exit(1)
	}

	files, err := collectBruFiles(*inputDir)
	if err != nil {
		fmt.Println("Error reading Bruno directory:", err)
		os.Exit(1)
	}

	requests := []Request{}
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			fmt.Println("Error reading file:", file, err)
			os.Exit(1)
		}
		parsed := parseBru(string(content))
		rel, _ := filepath.Rel(*inputDir, filepath.Dir(file))
		rel = filepath.ToSlash(rel)
		if rel != "." {
			parsed.Tag = rel
		}
		requests = append(requests, parsed)
	}

	openapi := buildOpenAPI(requests)
	yamlOut, err := yaml.Marshal(openapi)
	if err != nil {
		fmt.Println("Error generating YAML:", err)
		os.Exit(1)
	}

	if err := os.WriteFile(*outputFile, yamlOut, 0644); err != nil {
		fmt.Println("Error writing output:", err)
		os.Exit(1)
	}

	fmt.Println("✅ OpenAPI generated:", *outputFile)
}
