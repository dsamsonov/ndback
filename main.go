package main

import (
	"encoding/csv"
	"fmt"
	"github.com/google/goexpect"
	"github.com/naoina/toml"
	"github.com/pborman/getopt/v2"
	"google.golang.org/grpc/codes"
	"io"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	version = "0.0.1"
	author  = "Denis Samsonov (i@denjs.com)"
)

type tomlCfg struct {
	User, Password, DeviceDB, ConfigDir, LogFile string
	Type                                         map[string]TypeCfg
}

type TypeCfg struct {
	Method, Port                           string
	Timeout                                string
	Debug                                  bool
	UserPrompt, PwdPrompt, Prompt, Comment string
	CmdInventory, CmdConfig                []string
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

func create_backup_file(Hostname string, inventory, config []string) {
	cfgFile := cfg.ConfigDir + "/" + Hostname
	cf, err := os.Create(cfgFile)
	if err != nil {
		log.Printf("device %s, error while creating config file \"%s\": %s\n", Hostname, cfgFile, err)
		return
	}
	defer cf.Close()
	_, err = fmt.Fprintf(cf, "%s\n", inventory)
	if err != nil {
		log.Printf("device %s, error while write config file \"%s\": %s\n", Hostname, cfgFile, err)
		return
	}
	_, err = fmt.Fprintf(cf, "%s\n", config)
	if err != nil {
		log.Printf("device %s, error while write config file \"%s\": %s\n", Hostname, cfgFile, err)
		return
	}

}

func comment_string(input []string, symbol string) []string {
	out := make([]string, 0)
	for i := 0; i < len(input); i++ {
		for _, line := range strings.Split(input[i], "\n") {
			line := symbol + line
			out = append(out, line)

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
		result, _, err := e.Expect(promptRE, Timeout)
		if err != nil {
			log.Printf("device %s, error after sending command \"%s\": %s\n", Hostname, Commands[i], err)
			return out
		}
		out = append(out, result)
	}
	return out
}

func backup_device(Hostname, Address, DevType string, optDebug bool) {
	var (
		e       *expect.GExpect
		Timeout time.Duration
		err     error
	)
	fmt.Printf("Connecting to %s, %s type %s\n", Hostname, Address, DevType)
	log.Printf("Connecting to %s, %s type %s\n", Hostname, Address, DevType)
	promptRE := regexp.MustCompile(cfg.Type[DevType].Prompt)
	userRE := regexp.MustCompile(cfg.Type[DevType].UserPrompt)
	passRE := regexp.MustCompile(cfg.Type[DevType].PwdPrompt)
	if cfg.Type[DevType].Timeout == "" {
		Timeout = 60 * time.Second
	} else {
		s, _ := strconv.Atoi(cfg.Type[DevType].Timeout)
		Timeout = time.Duration(s) * time.Second
	}
	//connection to device
	if cfg.Type[DevType].Method == "telnet" {
		e, _, err = expect.Spawn(fmt.Sprintf("telnet %s %s", Address, cfg.Type[DevType].Port), -1, expect.Verbose(optDebug), expect.VerboseWriter(os.Stdout))
	}
	if cfg.Type[DevType].Method == "ssh" {
		e, _, err = expect.Spawn(fmt.Sprintf("ssh -F /dev/null -o UserKnownHostsFile=/dev/null -o StricthostKeyChecking=false -p %s -l %s %s", cfg.Type[DevType].Port, cfg.User, Address), -1, expect.Verbose(optDebug), expect.VerboseWriter(os.Stdout))
	}
	if err != nil {
		log.Printf("device %s, error: %s\n", Hostname, err)
		return
	}
	defer e.Close()

	//login to device
	_, _, _, err = e.ExpectSwitchCase([]expect.Caser{
		&expect.Case{R: userRE, S: cfg.User + "\n", T: expect.Continue(expect.NewStatus(codes.PermissionDenied, "Access denied, wrong username")), Rt: 2},
		&expect.Case{R: passRE, S: cfg.Password + "\n", T: expect.Continue(expect.NewStatus(codes.PermissionDenied, "Access denied, wrong password")), Rt: 2},
		&expect.Case{R: promptRE, T: expect.OK()},
	}, Timeout)
	if err != nil {
		log.Printf("device %s, error: %s\n", Hostname, err)
		return
	}

	//get inventory and add comment symbol to output
	result := runcmd_device(cfg.Type[DevType].CmdInventory, e, Hostname, promptRE, Timeout)
	inventory := comment_string(result, cfg.Type[DevType].Comment)
	fmt.Printf("%s\n", inventory)
	config := runcmd_device(cfg.Type[DevType].CmdConfig, e, Hostname, promptRE, Timeout)
	fmt.Printf("%s\n", config)
	create_backup_file(Hostname, inventory, config)
}

func main() {
	var (
		db [][]string
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
	//create log
	lf, err := os.Create(cfg.LogFile)
	if err != nil {
		Fatal(err)
	}
	log.SetOutput(lf)
	defer lf.Close()

	//go to hw
	for i := 0; i < len(db); i++ {
		Hostname := db[i][0]
		Address := db[i][1]
		DevType := db[i][2]
		backup_device(Hostname, Address, DevType, *optDebug)
	}
}
