package main

import (
	"errors"
	rotatelogs "github.com/lestrrat-go/file-rotatelogs"
	"github.com/rifflock/lfshook"
	log "github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"time"
)

const (
	DefaultTimeoutDuration time.Duration = 5000
	EvnMirrorPort          string        = "MIRROR_PORT"
	EvnMirrorConfigFile    string        = "MIRROR_CONFIG_FILE"
	PathMatchTypeExact     string        = "exact"
	PathMatchTypePrefix    string        = "prefix"
	PathMatchTypeRegexp    string        = "regexp"
)

type ServerProjectConfig struct {
	Port         int           `yaml:"port"`
	ProxyConfigs []ProxyConfig `yaml:"proxyConfig"`
}

type ProxyConfig struct {
	Desc   string            `yaml:"desc"`
	Paths  []ProxyPathConfig `yaml:"paths"`
	Hosts  []ProxyHostConfig `yaml:"hosts"`
	Filter ProxyConfigFilter `yaml:"filter"`
}

type ProxyPathConfig struct {
	Path      string `yaml:"path"`
	MatchType string `yaml:"matchType"`
	Remove    string `yaml:"remove"`
}

type ProxyHostConfig struct {
	Host   string `yaml:"host"`
	Weight int    `yaml:"weight"`
}

type ProxyConfigFilter struct {
	TimeOut          int      `yaml:"timeOut"`
	LimitHosts       int      `yaml:"limitHosts"`
	LimitQps         int      `yaml:"limitQps"`
	LimitRespHeaders []string `yaml:"limitRespHeaders"`
	Limiter          *rate.Limiter
}

func (x ProxyConfig) isEmpty() bool {
	return reflect.DeepEqual(x, ProxyConfig{})
}

func (p ProxyPathConfig) isExactMatchType() bool {
	return p.MatchType == PathMatchTypeExact
}
func (p ProxyPathConfig) isPrefixMatchType() bool {
	return p.MatchType == PathMatchTypePrefix
}
func (p ProxyPathConfig) isRegexpMatchType() bool {
	return p.MatchType == PathMatchTypeRegexp
}

type ProxyHostConfigs []ProxyHostConfig

func (s ProxyHostConfigs) Len() int { return len(s) }

func (s ProxyHostConfigs) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

func (s ProxyHostConfigs) Less(i, j int) bool { return s[i].Weight > s[j].Weight }

var (
	ProjectConfig ServerProjectConfig
	HttpTransport http.RoundTripper = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
)

func initLog() {

	// ?????????????????????json??????
	// force colors on for TextFormatter
	log.SetFormatter(&log.TextFormatter{
		EnvironmentOverrideColors: true,
		ForceColors:               true,
		FullTimestamp:             true,
		TimestampFormat:           "2006-01-02 15:04:05",
	})

	// ?????????????????????????????????????????????????????????stderr??????????????????
	// ????????????????????????????????????io.writer??????
	log.SetOutput(os.Stdout)
	// logfile, _ := os.OpenFile("./app.log", os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	// logrus.SetOutput(logfile)

	//????????????????????????
	//writers := []io.Writer{os.Stdout, logfile}
	//fileAndStdoutWriter := io.MultiWriter(writers...)
	//log.SetOutput(fileAndStdoutWriter)

	// ?????????????????????????????????????????????????????????stderr??????????????????
	// ????????????????????????????????????io.writer??????
	path := "logs/api-mirror.log"
	// ???????????????????????? 1 ?????????????????????????????????????????? 48 ???????????????????????????????????????????????????
	fileWriter, _ := rotatelogs.New(
		path+".%Y-%m-%d_%H",
		// `WithLinkName` ?????????????????????????????????
		rotatelogs.WithLinkName(path),
		// WithRotationTime ???????????????????????????????????????????????????
		// WithMaxAge ??????????????????????????????????????????
		// WithMaxAge ??? WithRotationCount????????????????????????
		rotatelogs.WithRotationTime(time.Duration(1)*time.Hour),
		rotatelogs.WithMaxAge(time.Duration(48)*time.Hour),
		//  `WithRotationCount` ??????????????????????????????????????????
	)

	log.AddHook(lfshook.NewHook(fileWriter, &log.TextFormatter{DisableColors: true, TimestampFormat: "2006-01-02 15:04:05"}))

	// ?????????????????????warn??????
	// logrus.SetLevel(logrus.InfoLevel)
	log.SetLevel(log.DebugLevel)

	// ?????????????????????????????????????????????????????????
	//log.SetReportCaller(true)
}

func initConfig(configFile string) {
	config, _, err := getConfigContent(configFile)
	if err != nil {
		log.Error(err)
	}

	// yaml?????????????????????????????????
	err1 := yaml.Unmarshal(config, &ProjectConfig)
	if err1 != nil {
		log.Error("config.yaml ???????????????", err1)
	}

	// ?????????????????????
	for index := range ProjectConfig.ProxyConfigs {
		log.Infof("init HandleFunc desc:[%s], filter:[%+v]", ProjectConfig.ProxyConfigs[index].Desc, ProjectConfig.ProxyConfigs[index].Filter)

		// ????????????????????????????????????
		ProjectConfig.ProxyConfigs[index].Filter.LimitRespHeaders = append(ProjectConfig.ProxyConfigs[index].Filter.LimitRespHeaders, "Content-Encoding")

		// ???????????????
		if ProjectConfig.ProxyConfigs[index].Filter.LimitQps > 0 {
			ProjectConfig.ProxyConfigs[index].Filter.Limiter = rate.NewLimiter(rate.Limit(ProjectConfig.ProxyConfigs[index].Filter.LimitQps), ProjectConfig.ProxyConfigs[index].Filter.LimitQps)
		} else {
			ProjectConfig.ProxyConfigs[index].Filter.Limiter = nil
		}
		// ??????path???????????????
		for i := range ProjectConfig.ProxyConfigs[index].Paths {
			// ????????????
			ProjectConfig.ProxyConfigs[index].Paths[i].MatchType = strings.TrimSpace(strings.ToLower(ProjectConfig.ProxyConfigs[index].Paths[i].MatchType))
			if len(ProjectConfig.ProxyConfigs[index].Paths[i].MatchType) == 0 {
				ProjectConfig.ProxyConfigs[index].Paths[i].MatchType = PathMatchTypeExact
			}
			if !strings.Contains(ProjectConfig.ProxyConfigs[index].Paths[i].MatchType, PathMatchTypeExact) &&
				!strings.Contains(ProjectConfig.ProxyConfigs[index].Paths[i].MatchType, PathMatchTypePrefix) &&
				!strings.Contains(ProjectConfig.ProxyConfigs[index].Paths[i].MatchType, PathMatchTypeRegexp) {
				log.Errorf("desc:[%s],path:[%s],?????????????????????matchType???[%s]", ProjectConfig.ProxyConfigs[index].Desc, ProjectConfig.ProxyConfigs[index].Paths[i].Path, ProjectConfig.ProxyConfigs[index].Paths[i].MatchType)
			}
			log.Infof("add HandleFunc success, desc:[%s], path:[%s], matchType???[%s]",
				ProjectConfig.ProxyConfigs[index].Desc,
				ProjectConfig.ProxyConfigs[index].Paths[i].Path,
				ProjectConfig.ProxyConfigs[index].Paths[i].MatchType)
		}
	}

}

// ???config??????????????????????????????????????????http???????????????
func getConfigContent(configFileStr string) ([]byte, string, error) {
	var content []byte
	var filePath string
	var configErr error

	configFiles := strings.Split(configFileStr, ",")
	for _, configFile := range configFiles {
		if len(strings.TrimSpace(configFile)) == 0 {
			continue
		}
		filePath = configFile
		if strings.HasPrefix(configFile, "http") {
			// ???????????????
			content, _ = getRequestByAll(filePath, "GET", nil, nil, 5000)
			if len(content) > 10 && string(content) != "httpError" && strings.Contains(string(content), "port") {
				configErr = nil
				log.Infof("?????????????????????????????????configUrl:[%s]", filePath)
				break
			}
			configErr = errors.New("??????????????????????????????")
			log.Errorf("?????????????????????????????????configUrl:[%s]", filePath)
		} else {
			content, configErr = ioutil.ReadFile(filePath)
			if configErr == nil && len(content) > 0 && strings.Contains(string(content), "port") {
				log.Infof("?????????????????????????????????configPath:[%s]", filePath)
				break
			}
			configErr = errors.New("??????????????????????????????")
			log.Errorf("?????????????????????????????????configPath:[%s]", filePath)
		}
	}
	return content, filePath, configErr
}
