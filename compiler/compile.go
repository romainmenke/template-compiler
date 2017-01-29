package compiler

import (
	"fmt"
	"go/ast"
	html "html/template"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	text "text/template"
	"text/template/parse"

	"github.com/mh-cbon/template-compiler/compiled"
	"github.com/mh-cbon/template-tree-simplifier/simplifier"
	"github.com/serenize/snaker"
)

// CompiledTemplatesProgram ...
type CompiledTemplatesProgram struct {
	varName      string
	imports      []*ast.ImportSpec
	funcs        []*ast.FuncDecl
	idents       []string
	builtinTexts map[string]string
}

// NewCompiledTemplatesProgram ...
func NewCompiledTemplatesProgram(varName string /*, conf *compiled.Configuration*/) *CompiledTemplatesProgram {
	ret := &CompiledTemplatesProgram{
		varName: varName,
		// config:  conf,
		idents: []string{
			"t", "w", "data", "indata", varName,
		},
		builtinTexts: map[string]string{},
	}
	ret.addImport("io")
	ret.addImport("github.com/mh-cbon/template-compiler/std/text/template/parse")
	return ret
}

// CompileAndWrite ...
func (c *CompiledTemplatesProgram) CompileAndWrite(config *compiled.Configuration) error {
	program, err := c.Compile(config)
	if err != nil {
		return err
	}
	if err := ioutil.WriteFile(config.OutPath, []byte(program), os.ModePerm); err != nil {
		return fmt.Errorf("Failed to write the compiled templates: %v", err)
	}
	return nil
}

//Compile ....
func (c *CompiledTemplatesProgram) Compile(config *compiled.Configuration) (string, error) {
	if err := updateOutPkg(config); err != nil {
		return "", err
	}

	templatesToCompile, err := c.getTemplatesToCompile(config)
	if err != nil {
		return "", err
	}

	return c.compileTemplates(config.OutPkg, templatesToCompile)
}

//compileTemplates ....
func (c *CompiledTemplatesProgram) compileTemplates(outpkg string, templatesToCompile []*TemplateToCompile) (string, error) {
	if err := c.convertTemplates(templatesToCompile); err != nil {
		return "", err
	}
	return c.generateProgram(outpkg, templatesToCompile), nil
}
func (c *CompiledTemplatesProgram) convertTemplates(templatesToCompile []*TemplateToCompile) error {
	for _, t := range templatesToCompile {
		for _, f := range t.files {
			for _, name := range f.names() {
				f.tplsFunc[name] = c.makeFuncName(f.tplsFunc[name])
				f.tplsFunc[name] = snakeToCamel(f.tplsFunc[name])

				err := convertTplTree(
					f.tplsFunc[name],
					f.tplsTree[name],
					t.FuncsExport,
					t.PublicIdents,
					t.DataConfiguration,
					f.tplsTypeCheck[name],
					c,
				)
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ...
func (c *CompiledTemplatesProgram) getTemplatesToCompile(conf *compiled.Configuration) ([]*TemplateToCompile, error) {
	templatesToCompile := convertConfigToTemplatesToCompile(conf)
	for _, t := range templatesToCompile {
		if err := t.prepare(); err != nil {
			return templatesToCompile, err
		}
	}
	return templatesToCompile, nil
}

func updateOutPkg(conf *compiled.Configuration) error {
	if conf.OutPkg == "" {
		pkgName, err := LookupPackageName(conf.OutPath)
		if err != nil {
			return fmt.Errorf("Failed to lookup for the package name: %v", err)
		}
		conf.OutPkg = pkgName
	}
	return nil
}

func (c *CompiledTemplatesProgram) getDataQualifier(dataConf compiled.DataConfiguration) string {
	dataAlias := c.addImport(dataConf.PkgPath)
	dataQualifier := fmt.Sprintf("%v.%v", dataAlias, dataConf.DataTypeName)
	if dataConf.IsPtr {
		dataQualifier = fmt.Sprintf("*%v.%v", dataAlias, dataConf.DataTypeName)
	}
	return dataQualifier
}

func (c *CompiledTemplatesProgram) addImport(pkgpath string) string {
	qpath := fmt.Sprintf("%q", pkgpath)
	bpath := filepath.Base(pkgpath)
	// if already imported, return the current alias
	for _, i := range c.imports {
		if i.Path.Value == qpath {
			if i.Name == nil {
				return bpath
			}
			return i.Name.Name
		}
	}
	newImport := &ast.ImportSpec{
		Path: &ast.BasicLit{Value: qpath},
	}
	c.imports = append(c.imports, newImport)
	if c.isCollidingIdent(bpath) {
		bpath = "alias" + bpath
		newImport.Name = &ast.Ident{Name: bpath}
	}
	c.idents = append(c.idents, bpath)
	return bpath
}

func (c *CompiledTemplatesProgram) isCollidingIdent(ident string) bool {
	// check for imports
	for _, i := range c.imports {
		if i.Name != nil && i.Name.Name == ident {
			return true
		}
	}
	// check for static idents
	for _, i := range c.idents {
		if i == ident {
			return true
		}
	}
	return false
}

func (c *CompiledTemplatesProgram) makeFuncName(baseName string) string {
	x := baseName
	i := 0
	for c.isCollidingIdent(x) {
		x = fmt.Sprintf("%v%v%v", "fn", i, baseName)
		i++
	}
	c.idents = append(c.idents, x)
	return x
}

func (c *CompiledTemplatesProgram) createFunc(name string) *ast.FuncDecl {
	gocode := fmt.Sprintf(
		`package aa
func %v(t parse.Templater, w io.Writer, indata interface{}) error {}`,
		name,
	)
	f := stringToAst(gocode)
	fn := f.Decls[0].(*ast.FuncDecl)
	c.funcs = append(c.funcs, fn)
	return fn
}

func (c *CompiledTemplatesProgram) addBuiltintText(text string) string {
	if x, ok := c.builtinTexts[text]; ok {
		return x
	}
	c.builtinTexts[text] = fmt.Sprintf("%v%v", "builtin", len(c.builtinTexts))
	return c.builtinTexts[text]
}

func (c *CompiledTemplatesProgram) generateInitFunc(tpls []*TemplateToCompile) string {
	initfunc := ""
	initfunc += fmt.Sprintf("func init () {\n")
	for _, t := range tpls {
		for _, f := range t.files {
			for _, name := range f.names() {
				funcname := f.tplsFunc[name]
				initfunc += fmt.Sprintf("  %v.Add(%#v, %v)\n", c.varName, name, funcname)
			}
		}
	}

	for _, t := range tpls {
		for i, f := range t.files {
			for e, name := range f.definedTemplates {
				varX := fmt.Sprintf("tpl%vX%v", i, e)
				varY := fmt.Sprintf("tpl%vY%v", i, e)
				initfunc += fmt.Sprintf("  %v := %v.MustGet(%#v)\n", varX, c.varName, f.name)
				initfunc += fmt.Sprintf("  %v := %v.MustGet(%#v)\n", varY, c.varName, name)
				initfunc += fmt.Sprintf("  %v, _ = %v.Compiled(%v)\n", varX, varX, varY)
				initfunc += fmt.Sprintf("  %v.Set(%#v, %v)\n", c.varName, f.name, varX)
			}
		}
	}

	initfunc += fmt.Sprintf("}")
	return initfunc
}
func (c *CompiledTemplatesProgram) generateProgram(outpkg string, tpls []*TemplateToCompile) string {
	program := fmt.Sprintf("package %v\n\n", outpkg)
	program += fmt.Sprintf("//golint:ignore\n\n")
	program += fmt.Sprintf("%v\n\n", c.generateImportStmt())
	program += fmt.Sprintf("%v\n\n", c.generateBuiltins())
	program += fmt.Sprintf("%v\n\n", c.generateInitFunc(tpls))
	for _, f := range c.funcs {
		program += fmt.Sprintf("%v\n\n", astNodeToString(f))
	}
	return program
}

func (c *CompiledTemplatesProgram) generateImportStmt() string {
	importStmt := ""
	importStmt += fmt.Sprintf("import (\n")
	for _, i := range c.imports {
		importStmt += fmt.Sprintf("\t")
		if i.Name != nil {
			importStmt += fmt.Sprintf("%v ", i.Name.Name)
		}
		importStmt += fmt.Sprintf("%v\n", i.Path.Value)
	}
	importStmt += fmt.Sprintf(")")
	return importStmt
}

func (c *CompiledTemplatesProgram) generateBuiltins() string {
	builtins := ""
	for text, name := range c.builtinTexts {
		builtins += fmt.Sprintf("var %v = []byte(%q)\n", name, text)
	}
	return builtins
}

func convertConfigToTemplatesToCompile(conf *compiled.Configuration) []*TemplateToCompile {
	ret := []*TemplateToCompile{}
	for _, t := range conf.Templates {
		ret = append(ret,
			makeTemplateToCompileNew(t),
		)
	}
	return ret
}

// TemplateToCompile ...
type TemplateToCompile struct {
	*compiled.TemplateConfiguration
	files []TemplateFileToCompile
}

// TemplateFileToCompile ...
type TemplateFileToCompile struct {
	name             string
	tplsTree         map[string]*parse.Tree
	tplsFunc         map[string]string
	tplsTypeCheck    map[string]*simplifier.State
	definedTemplates []string
}

func (t TemplateFileToCompile) names() []string {
	strs := []string{}
	for name := range t.tplsTree {
		strs = append(strs, name)
	}
	sort.Strings(strs)
	return strs
}

func makeTemplateToCompileNew(templateConf compiled.TemplateConfiguration) *TemplateToCompile {
	ret := &TemplateToCompile{
		TemplateConfiguration: &templateConf,
		files: []TemplateFileToCompile{},
	}
	return ret
}

func (t *TemplateToCompile) prepare() error {
	tplsPath, err := filepath.Glob(t.TemplatesPath)
	if err != nil {
		return fmt.Errorf("Failed to glob the templates: %v %v", t.TemplatesPath, err)
	}
	for _, tplPath := range tplsPath {
		fileTpl, err := makeTemplateFileToCompileFromFile(tplPath, t.Data, t.FuncsExport, t.HTML)
		if err != nil {
			return err
		}
		t.files = append(t.files, fileTpl)
	}
	return nil
}

func makeTemplateFileToCompileFromFile(tplPath string, data interface{}, funcs map[string]interface{}, HTML bool) (TemplateFileToCompile, error) {

	fileTpl := TemplateFileToCompile{
		name:             filepath.Base(tplPath),
		tplsTree:         map[string]*parse.Tree{},
		tplsFunc:         map[string]string{},
		tplsTypeCheck:    map[string]*simplifier.State{},
		definedTemplates: []string{},
	}

	content, err := ioutil.ReadFile(tplPath)
	if err != nil {
		return fileTpl, err
	}
	mainName := fileTpl.name

	var treeNames map[string]*parse.Tree
	if HTML {
		treeNames, err = compileHTMLTemplate(mainName, string(content), funcs)
	} else {
		treeNames, err = compileTextTemplate(mainName, string(content), funcs)
	}
	if err != nil {
		return fileTpl, err
	}
	fileTpl.tplsTree = treeNames
	for treeName, tree := range fileTpl.tplsTree {
		fileTpl.tplsTypeCheck[treeName] = simplifier.TransformTree(tree, data, funcs)
		if treeName != mainName {
			fileTpl.tplsFunc[treeName] = cleanTplName("fn" + mainName + "_" + treeName)
			fileTpl.definedTemplates = append(fileTpl.definedTemplates, treeName)
		} else {
			fileTpl.tplsFunc[treeName] = cleanTplName("fn" + mainName)
		}
	}
	return fileTpl, nil
}

func makeTemplateFileToCompileFromStr(name, tplContent string, data interface{}, funcs map[string]interface{}, HTML bool) (TemplateFileToCompile, error) {

	fileTpl := TemplateFileToCompile{
		name:             name,
		tplsTree:         map[string]*parse.Tree{},
		tplsFunc:         map[string]string{},
		tplsTypeCheck:    map[string]*simplifier.State{},
		definedTemplates: []string{},
	}

	var err error
	var treeNames map[string]*parse.Tree
	if HTML {
		treeNames, err = compileHTMLTemplate(name, tplContent, funcs)
	} else {
		treeNames, err = compileTextTemplate(name, tplContent, funcs)
	}
	if err != nil {
		return fileTpl, err
	}
	fileTpl.tplsTree = treeNames
	mainName := fileTpl.name
	for treeName, tree := range fileTpl.tplsTree {
		fileTpl.tplsTypeCheck[treeName] = simplifier.TransformTree(tree, data, funcs)
		if treeName != mainName {
			fileTpl.tplsFunc[treeName] = cleanTplName("fn" + mainName + "_" + treeName)
			fileTpl.definedTemplates = append(fileTpl.definedTemplates, treeName)
		} else {
			fileTpl.tplsFunc[treeName] = cleanTplName("fn" + mainName)
		}
	}
	return fileTpl, nil
}

// compileTextTemplate compiles a file template as a text/template.
func compileTextTemplate(name string, content string, funcsMap map[string]interface{}) (map[string]*parse.Tree, error) {
	ret := map[string]*parse.Tree{}

	t, err := text.New(name).Funcs(funcsMap).Parse(content)
	if err != nil {
		return ret, err
	}

	for _, tpl := range t.Templates() {
		tpl.Execute(ioutil.Discard, nil) // ignore err, it is just to force parse.
		if tpl.Tree != nil {
			ret[tpl.Name()] = tpl.Tree
		}
	}

	return ret, nil
}

// compileHTMLTemplate compiles a file template as an html/template.
func compileHTMLTemplate(name string, content string, funcsMap map[string]interface{}) (map[string]*parse.Tree, error) {
	ret := map[string]*parse.Tree{}

	t, err := html.New(name).Funcs(funcsMap).Parse(content)
	if err != nil {
		return ret, err
	}

	for _, tpl := range t.Templates() {
		tpl.Execute(ioutil.Discard, nil) // ignore err, it is just to force parse.
		if tpl.Tree != nil {
			ret[tpl.Name()] = tpl.Tree
		}
	}

	return ret, nil
}

// LookupPackageName search a directory for its declaring package.
func LookupPackageName(someDir string) (string, error) {
	dir := filepath.Dir(someDir)
	// the dir must exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return "", err
	}
	files, err := filepath.Glob(filepath.Dir(someDir) + "/*.go")
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		// maybe it is pointing to an empty dir
		dir := filepath.Dir(someDir)
		// the package name will be basename of dir
		return filepath.Base(dir), nil
	}
	f := files[0]
	b, err := ioutil.ReadFile(f)
	if err != nil {
		return "", err
	}
	return LookupPackageNameFromStr(string(b)), nil
}

// LookupPackageNameFromStr extract the declaring package from given go source string.
func LookupPackageNameFromStr(gocode string) string {
	// improve this. really q&d.
	gocode = gocode[strings.Index(gocode, "package"):]
	gocode = gocode[0:strings.Index(gocode, "\n")]
	return strings.Split(gocode, "package ")[1]
}

func snakeToCamel(s string) string {
	s = snaker.SnakeToCamel(s)
	if len(s) > 0 {
		s = strings.ToLower(s[:1]) + s[1:]
	}
	return s
}

func cleanTplName(name string) string {
	return replaceAll(name, []string{"."}, "_")
}

func replaceAll(old string, removals []string, replacement string) string {
	for _, r := range removals {
		old = strings.Replace(old, r, replacement, -1)
	}
	return old
}
