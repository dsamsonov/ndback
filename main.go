package main

import (
	"encoding/csv"
	"fmt"
	"github.com/google/goexpect"
	"github.com/naoina/toml"
	"github.com/pborman/getopt/v2"
	"github.com/zenthangplus/goccm"
	"google.golang.org/grpc/codes"
	"io"
	"log"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	version = "1.0"
	author  = "Denis Samsonov (i@denjs.com)"
)

type tomlCfg struct {
	User, Password, DeviceDB, ConfigDir, LogFile, Threads string
	Type                                                  map[string]TypeCfg
	Site                                                  map[string]SiteCfg
}

type TypeCfg struct {
	Method, Port                             string
	Timeout                                  string
	Debug                                    bool
	UserPrompt                               string
	PwdPrompt, Prompt, Comment               string
	CmdInventory, CmdConfig, UnwantedStrings []string
}

type SiteCfg struct {
	ConfigDir      string
	User, Password string
}

var (
	cfg tomlCfg
)

func Fatal(err error) {
	fmt.Printf("\nERROR! %s\n\n", err)
	os.Exit(1)
}

func parse_toml_config(cfg_file string) tomlCfg {
	var cfg tomlCfg
	file, err := os.Open(cfg_file)
	if err != nil {
		Fatal(err)
	}
	defer file.Close()
	if err := toml.NewDecoder(file).Decode(&cfg); err != nil {
		log.Fatal(err)
	}
	if cfg.User == "" {
		Fatal(fmt.Errorf("User is mandatory parameter in " + cfg_file))
	}
	if cfg.Password == "" {
		Fatal(fmt.Errorf("Password is mandatory parameter in " + cfg_file))
	}
	if cfg.DeviceDB == "" {
		Fatal(fmt.Errorf("DevicesDB is mandatory parameter in " + cfg_file))
	}
	if cfg.ConfigDir == "" {
		Fatal(fmt.Errorf("ConfigDir is mandatory parameter in " + cfg_file))
	}
	if cfg.LogFile == "" {
		Fatal(fmt.Errorf("LogFile is mandatory parameter in " + cfg_file))
	}
	return cfg
}

func parse_csv_devicedb(cfg_file string) [][]string {
	file, err := os.Open(cfg_file)
	if err != nil {
		Fatal(err)
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.Comma = ';'
	reader.Comment = '#'
	csvdb, err := reader.ReadAll()
	if err != nil {
		if err != io.EOF {
			Fatal(fmt.Errorf("error in %s, %s", cfg_file, err))
		}
	}
	return csvdb
}

func write_config(cf *os.File, input []string, Hostname, cfgFile string) {
	for i := 0; i < len(input); i++ {
		_, err := fmt.Fprintf(cf, "%s\n", input[i])
		if err != nil {
			log.Printf("device %s, error while write config file \"%s\": %s\n", Hostname, cfgFile, err)
			return
		}
	}
}

func check_unwanted_strings(array []string, str string) bool {
	for i := 0; i < len(array); i++ {
		re, err := regexp.MatchString(array[i], strings.TrimSpace(str))
		if err != nil {
			log.Printf("Parse unwanted strings error in %s: %s\n", array[i], err)
			return true
		}
		if re == true {
			return true
		}
	}
	return false
}

func prepare_string(input []string, comment, DevType string) []string {
	out := make([]string, 0)
	for i := 0; i < len(input); i++ {
		ss := strings.Split(input[i], "\n")
		for si := 0; si < len(ss); si++ {
			//ss[si] = strings.TrimSpace(ss[si])
			//unwanted prompt string
			prompt := []string{cfg.Type[DevType].Prompt}
			if check_unwanted_strings(prompt, ss[si]) == true {
				continue
			}
			//unwanted cmd inventory string
			if check_unwanted_strings(cfg.Type[DevType].CmdInventory, ss[si]) == true {
				continue
			}
			//unwanted cmd conf string
			if check_unwanted_strings(cfg.Type[DevType].CmdConfig, ss[si]) == true {
				continue
			}
			if check_unwanted_strings(cfg.Type[DevType].UnwantedStrings, ss[si]) == true {
				continue
			}
			string := fmt.Sprintf("%s%s", comment, ss[si])
			string = strings.TrimSpace(string)
			out = append(out, string)
		}
	}
	return out
}

func runcmd_device(Commands []string, e *expect.GExpect, Hostname string, promptRE *regexp.Regexp, Timeout time.Duration) []string {
	out := make([]string, 0)
	for i := 0; i < len(Commands); i++ {
		err := e.Send(Commands[i] + "\n\r")
		if err != nil {
			log.Printf("device %s, error while sending command \"%s\": %s\n", Hostname, Commands[i], err)
			return out
		}
		time.Sleep(1 * time.Second)
		result, _, err := e.Expect(promptRE, Timeout)
		if err != nil {
			log.Printf("device %s, error after sending command \"%s\": %s\n", Hostname, Commands[i], err)
			return out
		}
		out = append(out, result)
	}
	return out
}
func shell_backup_device(c goccm.ConcurrencyManager, Hostname, Address, DevType, Site string, optDebug bool) {
	var (
		e              *expect.GExpect
		Timeout        time.Duration
		err            error
		cfgDir         string
		user, password string
	)
	defer c.Done()
	userprompt := cfg.Type[DevType].UserPrompt
	if userprompt == "" {
		userprompt = "ogin:"
	}
	fmt.Printf("Connecting to %s, %s type %s\n", Hostname, Address, DevType)
	promptRE := regexp.MustCompile(cfg.Type[DevType].Prompt)
	userRE := regexp.MustCompile(userprompt)
	passRE := regexp.MustCompile(cfg.Type[DevType].PwdPrompt)
	if cfg.Type[DevType].Timeout == "" {
		Timeout = 60 * time.Second
	} else {
		s, _ := strconv.Atoi(cfg.Type[DevType].Timeout)
		Timeout = time.Duration(s) * time.Second
	}
	// user and password
	if cfg.Site[Site].User != "" {
		user = cfg.Site[Site].User
	} else {
		user = cfg.User
	}
	if cfg.Site[Site].Password != "" {
		password = cfg.Site[Site].Password
	} else {
		password = cfg.Password
	}
	//connection to device
	if cfg.Type[DevType].Method == "telnet" {
		e, _, err = expect.Spawn(fmt.Sprintf("telnet %s %s", Address, cfg.Type[DevType].Port), -1, expect.Verbose(optDebug), expect.VerboseWriter(os.Stdout))
	}
	if cfg.Type[DevType].Method == "ssh" {
		e, _, err = expect.Spawn(fmt.Sprintf("ssh -o UserKnownHostsFile=/dev/null -o StricthostKeyChecking=false -p %s -l %s %s", cfg.Type[DevType].Port, user, Address), -1, expect.Verbose(optDebug), expect.VerboseWriter(os.Stdout))
	}
	if err != nil {
		log.Printf("device %s, error: %s\n", Hostname, err)
		return
	}
	defer e.Close()

	//login to device
	_, _, _, err = e.ExpectSwitchCase([]expect.Caser{
		&expect.Case{R: userRE, S: user + "\n\r", T: expect.Continue(expect.NewStatus(codes.PermissionDenied, "Access denied, wrong username")), Rt: 2},
		&expect.Case{R: passRE, S: password + "\n\r", T: expect.Continue(expect.NewStatus(codes.PermissionDenied, "Access denied, wrong password")), Rt: 2},
		&expect.Case{R: promptRE, T: expect.OK()},
	}, Timeout)
	if err != nil {
		log.Printf("device %s, error: %s\n", Hostname, err)
		return
	}

	//get inventory and add comment symbol to output
	result := runcmd_device(cfg.Type[DevType].CmdInventory, e, Hostname, promptRE, Timeout)
	inventory := prepare_string(result, cfg.Type[DevType].Comment, DevType)
	time.Sleep(1 * time.Second)
	// get config
	result = runcmd_device(cfg.Type[DevType].CmdConfig, e, Hostname, promptRE, Timeout)
	config := prepare_string(result, "", DevType)
	// write to file
	if cfg.Site[Site].ConfigDir != "" {
		cfgDir = cfg.Site[Site].ConfigDir
	} else {
		cfgDir = cfg.ConfigDir
	}
	//check dir exist or create it
	if _, err := os.Stat(cfgDir); os.IsNotExist(err) {
		os.Mkdir(cfgDir, 0755)
	}
	cfgFile := cfgDir + "/" + Hostname
	cf, err := os.Create(cfgFile)
	if err != nil {
		log.Printf("device %s, error while creating config file \"%s\": %s\n", Hostname, cfgFile, err)
		return
	}
	defer cf.Close()
	write_config(cf, inventory, Hostname, cfgFile)
	write_config(cf, config, Hostname, cfgFile)
}

func main() {
	var (
		db      [][]string
		threads int
	)
	//parse command arguments
	optHelp := getopt.BoolLong("help", 'h', "display help")
	optVersion := getopt.BoolLong("version", 'v', "display version")
	optConfig := getopt.StringLong("config", 'c', "./ndback.conf", "configuration file")
	optDebug := getopt.BoolLong("debug", 'd', "debug mode")
	getopt.Parse()
	if *optHelp {
		getopt.Usage()
		os.Exit(0)
	}
	if *optVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	cfg = parse_toml_config(*optConfig)   //parse config
	db = parse_csv_devicedb(cfg.DeviceDB) //parse devices database(csv file)
	//max parallel jobs setup
	if cfg.Threads == "" {
		threads = runtime.NumCPU() * 2
	} else {
		threads, _ = strconv.Atoi(cfg.Threads)
	}
	c := goccm.New(threads)
	//create log
	lf, err := os.Create(cfg.LogFile)
	if err != nil {
		Fatal(err)
	}
	log.SetOutput(lf)
	defer lf.Close()
	log.Printf("NDBack %s started. Backuping %d devices\n", version, len(db))
	//go to hw
	for i := 0; i < len(db); i++ {
		Hostname := db[i][0]
		Address := db[i][1]
		DevType := db[i][2]
		Site := db[i][3]
		if cfg.Type[DevType].Method == "" {
			log.Printf("device %s, error: No [type.%s] method in %s\n", Hostname, DevType, *optConfig)
			continue
		}
		if cfg.Type[DevType].Port == "" {
			log.Printf("device %s, error: No [type.%s] port in %s\n", Hostname, DevType, *optConfig)
			continue
		}
		c.Wait()
		if cfg.Type[DevType].Method == "telnet" || cfg.Type[DevType].Method == "ssh" {
			go shell_backup_device(c, Hostname, Address, DevType, Site, *optDebug)
		}
	}
	if c.RunningCount() != 0 {
		c.WaitAllDone()
	}
	log.Printf("NDBack ended\n")
}
