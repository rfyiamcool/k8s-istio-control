package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v2"
)

const (
	FLAG_ENABLE  = "enabled"
	FLAG_DISABLE = "disabled"
	FLAG_STATUS  = "status"

	KIND_DEPLOYMENT = "deployment"
	KIND_DAEMONSET  = "daemonsets"
	KIND_SERVICE    = "service"
	KIND_POD        = "pod"
	KIND_VS         = "vs"
	KIND_DR         = "dr"
	KIND_CONFIGMAP  = "configmap"

	OP_SERV_RUN = iota
	OP_SERV_GENERATE
)

var (
	staticSkipFolder = []string{
		"output",
		"scripts",
		"tools",
		"etc",
	}

	envFile    string
	logTailNum int
	logSince   string
	help       bool

	defaultWorkingPath, _ = os.Getwd()
	meshWorkingPath       = filepath.Join(defaultWorkingPath, "mesh")

	kindMap = map[string]bool{
		KIND_DEPLOYMENT: true,
		KIND_DAEMONSET:  true,
		KIND_SERVICE:    true,
		KIND_POD:        true,
		KIND_VS:         true,
		KIND_DR:         true,
		KIND_CONFIGMAP:  true,
	}
)

func init() {
	if !existFile("mesh") || !existFile("deps") {
		log.fault("please run control scripts in deploy top path")
	}
}

type config struct {
	OutputPath        string              `yaml:"output_path"`
	Vars              map[string]string   `yaml:"vars"`
	Service           []string            `yaml:"service"`
	ServiceGroup      map[string][]string `yaml:"service_group"`
	Enable            ControlTypes        `yaml:"enable"`
	Disable           ControlTypes        `yaml:"disable"`
	MustDeps          []string            `yaml:"must_deps"`
	SkipInjectService []string            `yaml:"skip_inject_service"`

	HighPriorityDeps []string `yaml:"high_priority_deps"`
	MidPriorityDeps  []string `yaml:"mid_priority_deps"`
	LowPriorityDeps  []string `yaml:"low_priority_deps"`
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
	NameSpace      string
	service        map[string]serviceTypes
	runningService map[string]bool
	enableService  []string
}

func newGenerator(cfg config) *generator {
	return &generator{
		cfg:            cfg,
		service:        make(map[string]serviceTypes),
		runningService: make(map[string]bool),
		enableService:  make([]string, 0),
	}
}

func (g *generator) init() {
	g.service = g.walkMeshDir(meshWorkingPath)
	g.enableService = g.sortPriorityService(
		g.fillMustDeps(
			g.getEnableService(),
		),
	)

	if sn, repeated := isArrayRepeat(g.enableService); repeated {
		log.fault("found service %s repeated", sn)
	}

	ns, _ := g.cfg.Vars["namespace"]
	if ns == "" {
		ns, _, _ = g.commandExecute("whoami", "/")
	}
	if ns == "" {
		log.fault("get user name failed")
	}

	g.NameSpace = ns
}

func (g *generator) selectAndCreateNS() {
	var (
		format, cmd string
		err         error
	)

	// query namespace
	format = "kubectl  get ns|awk '{ print $1 }'|grep -v grep|grep %s"
	cmd = fmt.Sprintf(format, g.NameSpace)
	stdout, _, err := g.commandExecute(cmd, "/")
	stdout = removeLine(stdout)
	if stdout == g.NameSpace && err == nil {
		return
	}

	// create
	format = "kubectl create namespace %s"
	cmd = fmt.Sprintf(format, g.NameSpace)
	stdout, stderr, err := g.commandExecute(cmd, "/")
	if err != nil {
		log.fault("create namespace failed, stdout: %s, stderr: %s, err: %s",
			stdout,
			stderr,
			err.Error(),
		)
	}

	log.info(stdout)
}

func (g *generator) run() {
	g.beforeRun()
	g.selectAndCreateNS()
	g.handleRun(OP_SERV_RUN)
	g.afterRun()
}

func (g *generator) generator() {
	g.beforeRun()
	g.handleRun(OP_SERV_GENERATE)
}

func (g *generator) beforeRun() {
	g.preClean()
	g.createShareStorage()
	g.syncDeps()
}

func (g *generator) afterRun() {
}

func (g *generator) createShareStorage() {
	err := os.Mkdir("/biss-dep", os.ModePerm) // share dep
	if !existFile("/biss-dep") {
		log.fault("create share storage failed, err: %s", err.Error())
	}
}

func (g *generator) handleRun(op int) {
	for _, sn := range g.enableService {
		if state, _ := g.runningService[sn]; state {
			continue
		}
		g.runningService[sn] = true

		serv, ok := g.service[sn]
		if !ok {
			log.error("not found service %s in dir", sn)
			errExit()
		}

		g.servGenerateCfg(serv)
		if op == OP_SERV_GENERATE {
			continue
		}

		g.servStart(serv.sname)
		log.color(green, "finish deploy service %s", sn)
	}
}

func (g *generator) fillMustDeps(sl []string) []string {
	var (
		wl = []string{}
	)

	for _, sn := range g.cfg.MustDeps {
		if isExistInList(sn, sl) {
			continue
		}
		wl = append(wl, sn)
	}

	// insert front
	return append(wl, sl...)
}

func (g *generator) sortPriorityService(sl []string) []string {
	var (
		base = []string{}
		high = []string{}
		mid  = []string{}
		low  = []string{}
	)

	fn := func(sn string, target *[]string, dl []string) bool {
		if isExistInList(sn, dl) {
			*target = append(*target, sn)
			return true
		}
		return false
	}

	for _, sn := range sl {
		if fn(sn, &high, g.cfg.HighPriorityDeps) {
			continue
		}
		if fn(sn, &mid, g.cfg.MidPriorityDeps) {
			continue
		}
		if fn(sn, &low, g.cfg.LowPriorityDeps) {
			continue
		}

		base = append(base, sn)
	}

	high = append(high, mid...)
	high = append(high, low...)
	high = append(high, base...)
	return high
}

// base sort and repeat service
func (g *generator) getEnableService() []string {
	var (
		serviceSet = map[string]bool{}
	)

	// default enable all services
	if len(g.cfg.Enable.Service) == 0 && len(g.cfg.Enable.ServiceGroup) == 0 {
		return g.cfg.Service
	}

	for _, sgn := range g.cfg.Enable.ServiceGroup {
		sl := g.cfg.ServiceGroup[sgn]
		for _, sn := range sl {
			serviceSet[sn] = true
		}
	}
	for _, sn := range g.cfg.Enable.Service {
		serviceSet[sn] = true
	}

	sl := []string{}
	for sn, _ := range serviceSet {
		sl = append(sl, sn)
	}

	return sl
}

func (g *generator) syncDeps() {
	var (
		cmd = "bash sync.sh"
	)

	stdout, stderr, err := g.commandExecute(cmd, meshWorkingPath)
	if err != nil {
		log.error("bash sync.sh failed in prepare, stdout: %s, stderr: %s, err: %s",
			stdout,
			stderr,
			err.Error(),
		)
		errExit()
	}

	// g.cmdPrint(serv.sname, cmd, stdout, stderr)
}

func (g *generator) servGenerateCfg(serv serviceTypes) {
	tmpls := []string{serv.configMap, serv.dm, serv.service, serv.istio}
	for _, tp := range tmpls {
		if tp == "" {
			continue
		}

		rfile := filepath.Join(meshWorkingPath, serv.sname, tp)
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

		// validate output config
		if vres, valid := g.validateSyntax(doc.String()); !valid {
			log.fault("validate cfg generated syntax failed, regexp: %s", vres)
			errExit()
		}

		// create dir, write target
		targetDir := filepath.Join(g.cfg.OutputPath, serv.sname)
		os.Mkdir(targetDir, os.ModePerm) // ignore created already

		targetFile := filepath.Join(g.cfg.OutputPath, serv.sname, tp)
		g.output(targetFile, doc.String())
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

func (g *generator) commandExecutePipe(cmd string) error {
	runner := exec.Command("bash", "-c", cmd)
	stdout, err := runner.StdoutPipe()
	if err != nil {
		return err
	}

	runner.Start()
	reader := bufio.NewReader(stdout)
	for {
		line, _, err := reader.ReadLine()
		if err != nil || io.EOF == err {
			break
		}

		lower := strings.ToLower(string(line))
		if strings.Contains(lower, "error") {
			log.error("%s", line)
			continue
		}

		log.info("%s", line)
	}

	runner.Wait()
	return nil
}

func (g *generator) preClean() {
	os.Remove(g.cfg.OutputPath)
	os.Mkdir(g.cfg.OutputPath, os.ModePerm)
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

	cmd := fmt.Sprintf("kubectl label namespace %s istio-injection=%s --overwrite",
		g.NameSpace,
		mark,
	)
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
	for _, sn := range g.enableService {
		g.servStart(sn)
	}
}

func (g *generator) servStart(sn string) {
	var (
		workPath       = filepath.Join(g.cfg.OutputPath, sn)
		stdout, stderr string
		err            error
	)

	// disable istio inject; first disable, after enable
	if isExistInList(sn, g.cfg.SkipInjectService) {
		g.controlIstioInject(FLAG_DISABLE, defaultWorkingPath)
		defer g.controlIstioInject(FLAG_ENABLE, defaultWorkingPath)
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

func (g *generator) stopAll() {
	var (
		cmd    string
		stdout string
		stderr string
	)

	kinds := []string{
		"deployment",
		"daemonsets",
		"pods",
		"service",
		"configmap",
		"vs",
		"dr",
	}

	forceFormat := "kubectl -n %s delete %s --all --force --grace-period=0"
	format := "kubectl -n %s delete %s --all"

	for _, kind := range kinds {
		switch kind {
		case "deployment", "daemonsets", "pods":
			cmd = fmt.Sprintf(forceFormat, g.NameSpace, kind)
		default:
			cmd = fmt.Sprintf(format, g.NameSpace, kind)
		}

		stdout, stderr, _ = g.commandExecute(cmd, "/")
		log.color(green, "delete namespace %s %s", g.NameSpace, kind)
		log.info("stop result, stdout: %s, stderr: %s", stdout, log.nullStdout(stderr))
	}

	if g.NameSpace != "default" && g.NameSpace != "" {
		cmd = fmt.Sprintf("kubectl delete namespace %s", g.NameSpace)
		stdout, _, _ = g.commandExecute(cmd, "/")
		log.info(stdout)
	}
}

func (g *generator) statusAll() {
	for kind, _ := range kindMap {
		g.status(kind)
	}
}

func (g *generator) status(kind string) {
	cmd := fmt.Sprintf("kubectl -n %s get %s",
		g.NameSpace,
		kind,
	)
	stdout, _, _ := g.commandExecute(cmd, "/")
	log.color(yellow, "show %s status:", kind)
	log.info(stdout + "\n")
}

func (g *generator) getPodsName(sn string) []string {
	cmd := fmt.Sprintf("kubectl -n %s get pods",
		g.NameSpace,
	)
	stdout, _, _ := g.commandExecute(cmd, "/")
	lines := strings.Split(stdout, "\n")

	pods := []string{}
	for _, line := range lines {
		// optimize pod random name code
		if !strings.HasPrefix(line, sn) {
			continue
		}

		pods = append(pods, sn)
	}
	return pods
}

func (g *generator) getNodePort() {
	cmd := fmt.Sprintf("kubectl -n %s get services",
		g.NameSpace,
	)
	stdout, _, _ := g.commandExecute(cmd, "/")
	lines := strings.Split(stdout, "\n")
	for idx, line := range lines {
		if idx == 0 {
			log.color(yellow, "%s \n", line)
			continue
		}

		if !strings.Contains(line, " NodePort ") {
			continue
		}

		log.info("%s", line)
	}
}

func (g *generator) logTail(sn string) {
	pods := g.getPodsName(sn)
	if len(pods) == 0 {
		log.error("not found service %s pods", sn)
		return
	}

	podName := pods[0]
	cmd := fmt.Sprintf("kubectl -n %s logs %s -c %s --tail=%d -f",
		g.NameSpace,
		podName,
		sn,
		logTailNum,
	)
	g.commandExecutePipe(cmd)
}

func (g *generator) walkMeshDir(path string) map[string]serviceTypes {
	var (
		fs = map[string]serviceTypes{}
	)

	for _, servName := range g.cfg.Service {
		if !existFile(filepath.Join(path, servName)) {
			log.fault("service %s dir not found, current_path: %s",
				servName, path,
			)
		}

		serv := g.walkSubDir(servName)
		fs[servName] = serv
	}

	return fs
}

func (g *generator) walkSubDir(sn string) serviceTypes {
	var (
		sd = serviceTypes{}
	)

	sd.sname = sn
	files, _ := ioutil.ReadDir(filepath.Join(meshWorkingPath, sn))
	if len(files) == 0 {
		log.error("service %s dir not contains *.yaml ?", sn)
	}
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
			sd.service = "service.yaml"

		case "configmap.yaml":
			sd.configMap = "configmap.yaml"

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

func removeLine(s string) string {
	return strings.Replace(s, "\n", "", -1)
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

const (
	blue = iota
	yellow
	green
	bpink
)

type logger struct{}

func (l *logger) nullStdout(stderr string) string {
	if stderr == "" {
		return "nil"
	}
	return stderr
}

func (l *logger) info(format string, args ...interface{}) {
	v := fmt.Sprintf(format, args...)
	fmt.Println(v)
}

func (l *logger) color(color int, format string, args ...interface{}) {
	var (
		v   = fmt.Sprintf(format, args...)
		str string
	)

	switch color {
	case blue:
		str = fmt.Sprintf("%c[%d;%d;%dm %s %c[0m", 0x1B, 36, 40, 1, v, 0x1B)
	case yellow:
		str = fmt.Sprintf("%c[%d;%d;%dm %s %c[0m", 0x1B, 33, 40, 4, v, 0x1B)
	case green:
		str = fmt.Sprintf("%c[%d;%d;%dm %s %c[0m", 0x1B, 32, 40, 1, v, 0x1B)
	case bpink:
		str = fmt.Sprintf("%c[%d;%d;%dm %s %c[0m", 0x1B, 30, 41, 5, v, 0x1B)
	}

	fmt.Println(str)
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

func isArrayRepeat(sl []string) (string, bool) {
	m := map[string]bool{}

	for _, sn := range sl {
		if _, ok := m[sn]; ok {
			return sn, true
		}
		m[sn] = true
	}

	return "", false
}

func printUsage() {
	fmt.Fprintf(os.Stdout, `
Usage of ./control [ option ] [ cmd args... ]:

[option]:
-env string
	env file
	(default: {pwd}/etc/test_env.yaml)
-tail int
	lines of recent log file to display
	(default: 150)
-since string
	only return logs newer than a relative duration like 5s, 2m, or 3h.
-h bool
	show help


[cmd argv]: (for all k8s resource)
# start all resource
./control start

# stop all resource
./control stop

# query all resource
./control status

# generate config by template and env
./control gen

# query nodePort
./control port

# query k8s service list
./control service

# query k8s pod list
./control pod


[cmd argv ...]:
./control log {service name}
./control start {service name}
./control stop {service name}
./control reload {service name}

`)

}

func flagParse() {
	flag.StringVar(&envFile, "env", "", "env file")
	flag.BoolVar(&help, "h", false, "show help")
	flag.IntVar(&logTailNum, "tail", 150, "log tail num")
	flag.StringVar(&logSince, "since", "", "log since")
	flag.Parse()

	if help {
		printUsage()
		os.Exit(0)
	}

	runEnv := os.Getenv("RUN_ENV")
	log.color(blue, "RUN_ENV: %s", runEnv)

	if envFile == "" && runEnv != "PROD" {
		envFile = "etc/test_env.yaml"
		log.info("envfile is null, reset load %s", envFile)
	}

	if envFile == "" && runEnv == "PROD" {
		log.fault("PROD must need input env file")
	}
}

func oneCommand(gen *generator, args ...string) {
	op := args[0]

	switch op {
	case "start":
		gen.run()

	case "stop":
		gen.stopAll()

	case "gen":
		gen.generator()

	case "restart":
		gen.stopAll()
		gen.run()

	case "ps", "status":
		gen.statusAll()

	case "pods", "pod":
		gen.status(KIND_POD)

	case "service", "services", "svc":
		gen.status(KIND_SERVICE)

	case "pull":
		log.info("auto pull new image")

	case "port":
		log.info("show benvoy nodeport")
		gen.getNodePort()

	default:
		log.fault("invalid args")
	}
}

func multiCommand(gen *generator, args ...string) {
	op := args[0]
	sn := args[1]

	switch op {
	case "start":
	case "stop":
	case "logs", "log":
		gen.logTail(sn)
	}
}

func main() {
	flagParse()

	cfg, err := parseConfig(envFile)
	if err != nil {
		log.info("parse env config, err: %s", err.Error())
		errExit()
	}

	args := flag.Args()
	gen := newGenerator(cfg)
	gen.init()

	if len(args) == 1 {
		oneCommand(gen, args...)
		return
	}

	if len(args) > 1 {
		multiCommand(gen, args...)
		return
	}
}

