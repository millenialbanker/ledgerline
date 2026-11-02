package ops

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ledgerline/internal/money"
)

// Client insights are rule-based: no AI model is ever asked. These tests
// fail if the insights code calls an AI provider, directly or through any
// function it calls in this package.

// aiEntryPoints are the functions that reach an AI model (ailog.go).
var aiEntryPoints = map[string]bool{"aiAsk": true, "aiReadDocument": true, "aiGate": true, "ReadDocument": true}

func TestInsightsAreRuleBased(t *testing.T) {
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	calls := map[string]map[string]bool{} // function → functions it calls
	var insightFuncs []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		isInsights := strings.HasPrefix(filepath.Base(f), "insight")
		if isInsights {
			for _, im := range af.Imports {
				if p := strings.Trim(im.Path.Value, `"`); p == "ledgerline/internal/help" || strings.Contains(p, "anthropic") || strings.Contains(p, "openai") || strings.Contains(p, "genai") {
					t.Errorf("%s imports %s, an AI package", f, p)
				}
			}
		}
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			name := fd.Name.Name
			if isInsights {
				insightFuncs = append(insightFuncs, name)
			}
			if calls[name] == nil {
				calls[name] = map[string]bool{}
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					switch fn := x.Fun.(type) {
					case *ast.Ident:
						calls[name][fn.Name] = true
					case *ast.SelectorExpr:
						calls[name][fn.Sel.Name] = true
					}
				case *ast.SelectorExpr:
					if isInsights && (x.Sel.Name == "LedgyAI" || x.Sel.Name == "Meter") {
						t.Errorf("%s uses o.%s (the AI model)", name, x.Sel.Name)
					}
				}
				return true
			})
		}
	}
	if len(insightFuncs) < 10 {
		t.Fatalf("found only %d insights functions", len(insightFuncs))
	}
	// Everything the insights code can reach, by name.
	seen := map[string]string{}
	queue := append([]string{}, insightFuncs...)
	for _, f := range insightFuncs {
		seen[f] = f
	}
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		for g := range calls[f] {
			if aiEntryPoints[g] {
				t.Errorf("client insights reach an AI model: %s → %s", seen[f], g)
			}
			if _, ok := seen[g]; !ok && calls[g] != nil {
				seen[g] = seen[f] + " → " + g
				queue = append(queue, g)
			}
		}
	}
	var names []string
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	t.Logf("insights reach %d functions, none of them an AI call", len(names))
}

func TestInsightsNeverCallAI(t *testing.T) {
	o, ctx := setup(t)
	w := &countingWriter{}
	o.LedgyAI, o.Meter = w, meter(t, "2", "")
	set, _ := o.Settings(ctx, T)
	set.LedgyAI, set.BillAI = true, true
	o.SaveSettings(ctx, T, "admin", set)
	o.AddAICredit(ctx, T, "provider", money.Amount(100000), "INV-1")
	if _, err := o.FindInsights(ctx, T, "priya.nair", D); err != nil {
		t.Fatal(err)
	}
	v, err := o.Insights(ctx, T, D, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range v.Items {
		if _, err := o.TaxPlan(ctx, T, i.UCC, D); err != nil {
			t.Fatal(err)
		}
		break
	}
	if w.calls != 0 || w.fakeReader.calls != 0 || o.aiLogCount(ctx, T, AIFeatureLedgy, "", "") != 0 {
		t.Fatalf("insights called the AI model %d times", w.calls+w.fakeReader.calls)
	}
}
