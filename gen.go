package main

import (
	"bytes"
	"flag"
	"fmt"
	"html/template"
	"io/ioutil"
	"os"
	"os/exec"
	"regexp"

	"gopkg.in/yaml.v2"
)

var (
	staticSkipFolder = []string{
		"output",
		"scripts",
		"tools",
		"etc",
	}

	envFile string
)

const (
	FLAG_ENABLE  = "enable"
	FLAG_DISABLE = "disable"
	FLAG_STATUS  = "status"
)

type config struct {
	OutputPath        string              `yaml:"output_path"`
	Vars              map[string]string   `yaml:"vars"`
	Service           []string            `yaml:"service"`
	ServiceGroup      map[string][]string `yaml:"service_group"`
	Enable            ControlTypes        `yaml:"enable"`
	Disable           ControlTypes        `yaml:"disable"`
	MustDeps          []string            `yaml:"must_deps"`
	SkipInjectService []string            `yaml:"skip_inject_service"`
}

type ControlTypes struct {
	Service      []string `yaml:"service"`
	ServiceGroup []string `yaml:"service_group"`
}

type serviceTypes struct {
	sname     string
	apply     string
	configMap string
	dm        string
	service   string // k8s service name
	istio     string
	sync      string
}

type generator struct {
	cfg            config
	service        map[string]serviceTypes
	runningService map[string]bool
	enableService  map[string]bool
}

func newGenerator(cfg config) *generator {
	return &generator{
		cfg:            cfg,
		service:        make(map[string]serviceTypes),
		runningService: make(map[string]bool),
		enableService:  make(map[string]bool),
	}
}

func (g *generator) init() {
	g.preClean()
	pwd, _ := os.Getwd()
	g.service = g.walkMeshDir(pwd)
	g.enableService = g.getEnableServices()
}

func (g *generator) run() {
	for sn, _ := range g.enableService {
		log.info("will deploy service %s", sn)

		if state, _ := g.runningService[sn]; state {
			continue
		}

		serv, ok := g.service[sn]
		if !ok {
			log.error("not found service %s in dir", sn)
			errExit()
		}

		g.prepare(serv)
		g.generateCfg(serv)
		g.start(serv.sname)

		log.info("finish deploy service %s", sn)
	}
}

func (g *generator) getEnableServices() map[string]bool {
	var (
		services = map[string]bool{}
	)

	if len(g.cfg.Enable.Service) == 0 && len(g.cfg.Enable.ServiceGroup) == 0 {
		for _, sn := range g.cfg.Service {
			services[sn] = true
		}
		return services
	}

	for _, sgn := range g.cfg.Enable.ServiceGroup {
		sl := g.cfg.ServiceGroup[sgn]
		for _, sn := range sl {
			services[sn] = true
		}
	}

	for _, sn := range g.cfg.Enable.Service {
		services[sn] = true
	}

	return services
}

func (g *generator) prepare(serv serviceTypes) {
	if serv.sync == "" {
		return
	}

	var (
		pwd, _ = os.Getwd()
		path   = pwd + "/" + serv.sname
		cmd    = "bash sync.sh"
	)

	stdout, stderr, err := g.commandExecute(cmd, path)
	if err != nil {
		log.error("service %s sync.sh failed in prepare, stdout: %s, stderr: %s, err: %s", serv.sname,
			stdout,
			stderr,
			err.Error(),
		)
		errExit()
	}
	// g.cmdPrint(serv.sname, cmd, stdout, stderr)
}

func (g *generator) generateCfg(serv serviceTypes) {
	tmpls := []string{serv.configMap, serv.dm, serv.service, serv.istio}
	for _, tp := range tmpls {
		if tp == "" {
			continue
		}

		rfile := serv.sname + "/" + tp
		if !existFile(rfile) {
			log.fault("%s not found", rfile)
			errExit()
		}

		tmp, err := template.ParseFiles(rfile)
		if err != nil {
			log.fault("config template parse failed, err: %s", err.Error())
			errExit()
		}

		var doc bytes.Buffer
		err = tmp.Execute(&doc, g.cfg.Vars)
		if err != nil {
			log.fault("config template execute failed, err: %s", err.Error())
			errExit()
		}

		os.Mkdir(g.cfg.OutputPath+"/"+serv.sname, os.ModePerm)

		// validate output config
		if vres, valid := g.validateSyntax(doc.String()); !valid {
			log.fault("validate cfg generated syntax failed, regexp: %s", vres)
			errExit()
		}

		target := g.cfg.OutputPath + "/" + serv.sname + "/" + tp
		g.output(target, doc.String())
	}
}

func (g *generator) output(target string, data string) {
	f, err := os.Create(target)
	if err != nil {
		log.error("create file %s failed, err: %s", target, err)
		errExit()
	}

	f.WriteString(data)
	f.Close()
}

func (g *generator) validateSyntax(tmpl string) (string, bool) {
	syntaxList := []*regexp.Regexp{
		regexp.MustCompile(`{{`),
		regexp.MustCompile(`}}`),
		regexp.MustCompile(`{{.*?}}`),
	}

	for _, synatx := range syntaxList {
		res := synatx.FindString(tmpl)
		if res == "" {
			continue
		}

		return res, false
	}

	return "", true
}

func (g *generator) ExistShareStorage() bool {
	return true
}

func (g *generator) commandExecute(cmd string, workPath string) (string, string, error) {
	var (
		stdout, stderr bytes.Buffer
		err            error
	)

	runner := exec.Command("bash", "-c", cmd)
	if workPath != "" {
		runner.Dir = workPath
	}
	runner.Stdout = &stdout
	runner.Stderr = &stderr
	err = runner.Start()
	if err != nil {
		return string(stdout.Bytes()), string(stderr.Bytes()), err
	}

	err = runner.Wait()
	if err != nil {
		return string(stdout.Bytes()), string(stderr.Bytes()), err
	}

	return string(stdout.Bytes()), string(stderr.Bytes()), err
}

func (g *generator) preClean() {
	os.Remove("output")
	os.Mkdir("output", os.ModePerm)
}

func (g *generator) cmdPrint(sn, cmd, stdout, stderr string) {
	log.info("service %s command %s finish, stdout: %s", sn, cmd, stdout)
	if stderr != "" {
		log.error("service %s command %s finish, err: %s", sn, cmd, stderr)
	}
}

func (g *generator) controlIstioInject(mark string, workPath string) {
	switch mark {
	case FLAG_ENABLE, FLAG_DISABLE, FLAG_STATUS:
		//
	default:
		log.fault("invalid type")
	}

	cmd := "./scripts/inject_control.sh " + mark
	stdout, stderr, err := g.commandExecute(
		cmd, workPath,
	)
	if err != nil {
		log.error("ops inject_control.sh failed, stdout: %s, stderr: %s, err: %s",
			stdout,
			stderr,
			err.Error(),
		)
		errExit()
	}

	g.cmdPrint("istio", cmd, stdout, stderr)
}

func (g *generator) iterateStart() {
	for sn, _ := range g.enableService {
		g.start(sn)
	}
}

func (g *generator) start(sn string) {
	var (
		pwd, _         = os.Getwd()
		workPath       = pwd + "/" + g.cfg.OutputPath + "/" + sn
		stdout, stderr string
		err            error
	)

	// disable istio inject; first disable, after enable
	if isExistInList(sn, g.cfg.SkipInjectService) {
		g.controlIstioInject(FLAG_DISABLE, pwd)
		defer g.controlIstioInject(FLAG_ENABLE, pwd)
	}

	serv := g.service[sn]
	cmd := "kubectl apply -f configmap.yaml "

	// first apply cm
	if serv.configMap != "" {
		stdout, stderr, err = g.commandExecute(
			cmd, workPath,
		)

		if err != nil {
			log.error("command execute failed, stdout: %s, stderr: %s, err: %s",
				stdout,
				stderr,
				err.Error(),
			)
			errExit()
		}
		g.cmdPrint(sn, cmd, stdout, stderr)
	}

	// apply *.yaml, contain unchanged configmap
	stdout, stderr, err = g.commandExecute(
		"kubectl apply -f .", workPath,
	)
	g.cmdPrint(sn, cmd, stdout, stderr)
	if err != nil {
		log.error("command execute failed, err: %s", err.Error())
		errExit()
	}
}

func (g *generator) stop() {
}

func (g *generator) reload() {
}

func (g *generator) restart() {
}

func (g *generator) walkMeshDir(path string) map[string]serviceTypes {
	var (
		fs = map[string]serviceTypes{}
	)

	files, _ := ioutil.ReadDir(path)
	for _, fi := range files {
		if isStaticSkipFolder(fi.Name()) {
			continue
		}
		if !fi.IsDir() {
			continue
		}

		fs[fi.Name()] = g.walkSubDir(fi.Name())
	}

	return fs
}

func (g *generator) walkSubDir(path string) serviceTypes {
	var (
		sd = serviceTypes{}
	)

	sd.sname = path
	files, _ := ioutil.ReadDir(path)
	for _, fi := range files {
		if fi.IsDir() {
			continue
		}

		// optimize ...
		switch fi.Name() {
		case "apply.sh":
			sd.apply = "apply.sh"

		case "dm.yaml":
			sd.dm = "dm.yaml"

		case "service.yaml":
			sd.service = ""

		case "configmap.yaml":
			sd.configMap = "configmap.yaml"

		case "sync.sh":
			sd.sync = "sync.sh"

		case "istio.yaml":
			sd.istio = "istio.yaml"
		}
	}

	return sd
}

func checkCurrentPath(path string) bool {
	return true
}

func isStaticSkipFolder(f string) bool {
	for _, sf := range staticSkipFolder {
		if sf != f {
			continue
		}
		return true
	}
	return false
}

func isExistInList(f string, list []string) bool {
	for _, sf := range list {
		if sf != f {
			continue
		}
		return true
	}
	return false
}

func checkBinPathExist(cmd string) bool {
	_, err := exec.LookPath(cmd)
	if err != nil {
		return false
	}
	return true
}

func parseConfig(f string) (config, error) {
	cfg := config{}
	data, err := ioutil.ReadFile(f)
	if err != nil {
		return cfg, err
	}

	err = yaml.Unmarshal(data, &cfg)
	return cfg, err
}

func errExit() {
	os.Exit(99)
}

var (
	log = new(logger)
)

type logger struct{}

func (l *logger) info(format string, args ...interface{}) {
	v := fmt.Sprintf(format, args...)
	fmt.Println(v)
}

func (l *logger) error(format string, args ...interface{}) {
	v := fmt.Sprintf(format, args...)
	color := fmt.Sprintf("%c[%d;%d;%dm %s %c[0m", 0x1B, 5, 40, 31, v, 0x1B)
	fmt.Println(color)
}

func (l *logger) fault(format string, args ...interface{}) {
	l.error(format, args...)
	errExit()
}

func existFile(name string) bool {
	if _, err := os.Stat(name); err != nil {
		if os.IsNotExist(err) {
			return false
		}
	}
	return true
}

func main() {
	flag.StringVar(&envFile, "env", "envfile", "env file")
	flag.Parse()

	cfg, err := parseConfig(envFile)
	if err != nil {
		log.info("parse env config, err: %s", err.Error())
		errExit()
	}

	gen := newGenerator(cfg)
	gen.init()
	gen.run()
}

