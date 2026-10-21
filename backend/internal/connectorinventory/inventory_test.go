package connectorinventory

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// This test checks the source inventory before any generated consumer exists.
// Listed eligibility does not activate a route or grant a caller access.
func TestInventoryCoversCurrentHTTPRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/connector/v1/operations.json")
	if err != nil {
		t.Fatal(err)
	}
	var inventory struct {
		Version    int                 `json:"version"`
		Columns    []string            `json:"columns"`
		Operations [][]json.RawMessage `json:"operations"`
	}
	if err := json.Unmarshal(raw, &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.Version != 1 || !slices.Equal(inventory.Columns,
		[]string{"operation", "destination", "capabilities", "delegationAllowed", "method", "route"}) {
		t.Fatal("unexpected inventory schema")
	}
	bindings := map[string]string{}
	tools := map[string]string{}
	policies := map[string]string{}
	for i, row := range inventory.Operations {
		if len(row) != 6 {
			t.Fatalf("row %d: expected six columns", i)
		}
		var operation, destination, method, route string
		var capabilities []string
		var allowed bool
		if json.Unmarshal(row[0], &operation) != nil || json.Unmarshal(row[1], &destination) != nil ||
			json.Unmarshal(row[2], &capabilities) != nil || json.Unmarshal(row[3], &allowed) != nil ||
			json.Unmarshal(row[4], &method) != nil || json.Unmarshal(row[5], &route) != nil ||
			string(row[3]) == "null" {
			t.Fatalf("row %d: invalid column type", i)
		}
		if operation == "" || route == "" ||
			!slices.Contains([]string{"ttobak-api-bridge", "ttobak-qa-bridge"}, destination) ||
			!slices.Contains([]string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE", "TOOL", "INTERNAL"}, method) {
			t.Fatalf("row %d: invalid operation binding", i)
		}
		if !slices.Contains(capabilities, "read") || !slices.IsSorted(capabilities) {
			t.Fatalf("row %d: capabilities must be sorted and include read", i)
		}
		for j, capability := range capabilities {
			if !slices.Contains([]string{"delete", "read", "share", "write"}, capability) ||
				(j > 0 && capability == capabilities[j-1]) {
				t.Fatalf("row %d: invalid or duplicate capability", i)
			}
		}
		policy := destination + "|" + strings.Join(capabilities, ",") + "|" + string(row[3])
		if prior, exists := policies[operation]; exists && prior != policy {
			t.Fatalf("inconsistent policy for %s", operation)
		}
		policies[operation] = policy
		if method == "TOOL" {
			if destination != "ttobak-qa-bridge" || tools[route] != "" {
				t.Fatalf("invalid/duplicate QA binding: %s", route)
			}
			tools[route] = operation
		}
		if destination == "ttobak-api-bridge" {
			binding := method + " " + route
			if _, exists := bindings[binding]; exists {
				t.Fatalf("duplicate HTTP binding: %s", binding)
			}
			bindings[binding] = operation
		}
	}
	file, err := parser.ParseFile(token.NewFileSet(), "../../cmd/api/main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	current := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if slices.Contains([]string{"Method", "MethodFunc", "Handle", "HandleFunc", "Mount", "Route"}, selector.Sel.Name) {
			t.Fatalf("unsupported route registration requires explicit coverage: %s", selector.Sel.Name)
		}
		if !slices.Contains([]string{"Get", "Post", "Put", "Patch", "Delete", "Head", "Options", "Connect", "Trace"}, selector.Sel.Name) {
			return true
		}
		if len(call.Args) == 0 {
			t.Fatal("route method without arguments")
		}
		path, ok := call.Args[0].(*ast.BasicLit)
		if !ok || path.Kind != token.STRING {
			t.Fatal("route coverage requires a literal path")
		}
		route, err := strconv.Unquote(path.Value)
		if err != nil {
			t.Fatal(err)
		}
		// Current chi routes are absolute literals. Fail if a new layout needs
		// path composition rather than silently treating a relative route as complete.
		if !strings.HasPrefix(route, "/api/") {
			t.Fatalf("unexpected route prefix: %s", route)
		}
		binding := strings.ToUpper(selector.Sel.Name) + " " + route
		if current[binding] {
			t.Fatalf("duplicate registered HTTP route: %s", binding)
		}
		current[binding] = true
		return true
	})
	if len(current) == 0 {
		t.Fatal("no HTTP routes inspected")
	}
	for binding := range current {
		if bindings[binding] == "" {
			t.Errorf("HTTP route missing explicit policy: %s", binding)
		}
	}
	for binding := range bindings {
		if !current[binding] {
			t.Errorf("inventory names unimplemented HTTP route: %s", binding)
		}
	}
	// Parse only the literal registry and its explicit starred source-tool list.
	// Do not import tools.py: importing would construct clients or execute code.
	command := exec.Command("python3", "-c", `
import ast, json, pathlib
root = pathlib.Path('../../python/qa')
def registry(filename, name, imports=None):
    tree = ast.parse((root / filename).read_text())
    if imports is not None:
        assert any(isinstance(n, ast.ImportFrom) and n.module == 'source_tools'
                   and any(a.name == 'SOURCE_TOOL_DEFINITIONS' and a.asname is None for a in n.names)
                   for n in tree.body), 'source registry import changed'
    values = [n.value for n in tree.body if isinstance(n, ast.Assign)
              and any(isinstance(t, ast.Name) and t.id == name for t in n.targets)]
    assert len(values) == 1 and isinstance(values[0], ast.List), 'registry shape changed'
    declarations = [n for n in tree.body if isinstance(n, ast.Assign)
                    and any(isinstance(t, ast.Name) and t.id == name for t in n.targets)]
    assert len(declarations[0].targets) == 1, 'registry alias declaration changed'
    allowed = set(ast.walk(declarations[0]))
    # The source module has one exact read-only name projection. All other
    # registry references need an explicit inspection update, not execution.
    projection = ast.parse("SOURCE_TOOL_NAMES = frozenset(tool['toolSpec']['name'] for tool in SOURCE_TOOL_DEFINITIONS)").body[0]
    for node in tree.body:
        if ast.dump(node) == ast.dump(projection):
            allowed.update(ast.walk(node))
    tracked = {name, *(imports or {})}
    for node in ast.walk(tree):
        if isinstance(node, ast.Name) and node.id in tracked:
            assert node in allowed, 'unsupported registry reference or mutation'
        if isinstance(node, ast.Attribute) and node.attr in tracked:
            raise AssertionError('unsupported qualified registry reference')
    result = []
    for node in values[0].elts:
        if isinstance(node, ast.Starred):
            assert imports is not None and isinstance(node.value, ast.Name), 'unknown registry splice'
            result.extend(imports[node.value.id])
        else:
            result.append(ast.literal_eval(node))
    return result
source = registry('source_tools.py', 'SOURCE_TOOL_DEFINITIONS')
tools = registry('tools.py', 'TOOL_DEFINITIONS', {'SOURCE_TOOL_DEFINITIONS': source})
print(json.dumps([tool['toolSpec']['name'] for tool in tools]))
`)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("cannot inspect actual QA registry: %v: %s", err, output)
	}
	var names []string
	if err := json.Unmarshal(output, &names); err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no QA tools inspected")
	}
	registered := map[string]bool{}
	for _, name := range names {
		if registered[name] {
			t.Fatalf("duplicate registered QA tool: %s", name)
		}
		registered[name] = true
		if tools[name] == "" {
			t.Errorf("QA tool missing explicit policy: %s", name)
		}
	}
	for name := range tools {
		if !registered[name] {
			t.Errorf("inventory names unimplemented QA tool: %s", name)
		}
	}
}
