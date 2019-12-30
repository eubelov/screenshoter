package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/google/uuid"

	"gopkg.in/yaml.v2"
)

type imageFormat struct {
	format string
}

type runOptions struct {
	width           int
	height          int
	inputFilePath   string
	delay           int
	outputDirectory string
	postfix         string
	useQueryParam   string
	sem             *semaphore.Weighted
	server          *config
	imageFormat
}

type config struct {
	Server struct {
		Host       string `yaml:"host"`
		Port       int    `yaml:"port"`
		PingPath   string `yaml:"pingPath"`
		ActionPath string `yaml:"actionPath"`
	} `yaml:"server"`
}

var (
	ctx           = context.TODO()
	width         = flag.Int("width", 1024, "Width of a screenshot")
	height        = flag.Int("height", 768, "Height of a screenshot")
	delay         = flag.Int("delay", 0, "Delay between full page load & taking a screenshot")
	filePath      = flag.String("file", "", "Absolute path to a file with URLs")
	outputPath    = flag.String("outputDir", "", "Output directory")
	postfix       = flag.String("postfix", "", "postfix")
	format        = flag.String("imageFormat", "jpeg", "Format of a screenshot (jpeg or png)")
	useQueryParam = flag.String("useQueryParam", "", "Use query parameter as file name")
	concurrency   = flag.Int("concurrency", 2, "Number of concurrent requests")
)

func main() {
	logger, logFile := setupLogToFile()
	defer logFile.Close()

	conf := readConfig(logger)
	logger.Printf("%+v", *conf)

	flag.Parse()

	opt := &runOptions{
		width:           *width,
		height:          *height,
		delay:           *delay,
		inputFilePath:   *filePath,
		outputDirectory: *outputPath,
		postfix:         *postfix,
		useQueryParam:   *useQueryParam,
		sem:             semaphore.NewWeighted(int64(*concurrency)),
		server:          conf,
		imageFormat: imageFormat{
			format: *format,
		},
	}

	logger.Printf("%+v\n", opt)
	checkServerAvailable(opt.server, logger)
	takeScreenshots(opt, logger)
}

func setupLogToFile() (l *log.Logger, f *os.File) {
	_ = os.Mkdir("logs", 0644)

	file, _ := os.OpenFile(fmt.Sprintf("logs/%s.log", uuid.New()), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	logger := log.New(io.MultiWriter(os.Stdout, file), "", log.LstdFlags)
	return logger, file
}

func readConfig(logger *log.Logger) *config {
	f, err := os.Open("config.yaml")
	if err != nil {
		logger.Panicf("config.yaml not found in binary directory: %v", err)
	}

	defer f.Close()
	var conf config
	dec := yaml.NewDecoder(f)
	if err = dec.Decode(&conf); err != nil {
		logger.Panicf("can't parse config.yaml: %v", err)
	}

	return &conf
}

func takeScreenshots(runOptions *runOptions, logger *log.Logger) {
	if file, err := os.Open(runOptions.inputFilePath); err != nil {
		logger.Panicf("file does not exist: %s", runOptions.inputFilePath)
	} else {
		defer file.Close()

		scanner := bufio.NewScanner(file)
		actionURL := fmt.Sprintf("%s:%d/%s", runOptions.server.Server.Host, runOptions.server.Server.Port, runOptions.server.Server.ActionPath)

		for scanner.Scan() {
			if err := runOptions.sem.Acquire(ctx, 1); err != nil {
				logger.Printf("failed to acquire semaphore: %v", err)
			}

			url := scanner.Text()

			go saveImage(runOptions, actionURL, url, logger)
		}

		if err := runOptions.sem.Acquire(ctx, int64(*concurrency)); err != nil {
			logger.Printf("failed to acquire semaphore: %v", err)
		}
	}
}

func saveImage(runOptions *runOptions, host, u string, logger *log.Logger) {
	start := time.Now()

	logger.Printf("processing %s", u)

	var fileName string
	defer runOptions.sem.Release(1)

	if runOptions.useQueryParam != "" {
		parsedURL, _ := url.Parse(u)
		fn := parsedURL.Query().Get(runOptions.useQueryParam)
		if fn != "" {
			fileName = fmt.Sprintf("%s%s.%s", fn, runOptions.postfix, runOptions.format)
		}
	}
	if fileName == "" {
		fileName = fmt.Sprintf("%s%s.%s", uuid.New(), runOptions.postfix, runOptions.format)
	}

	formData := url.Values{
		"TimeoutSeconds": {strconv.Itoa(runOptions.delay)},
		"FileName":       {fileName},
		"Url":            {u},
		"Width":          {strconv.Itoa(runOptions.width)},
		"Height":         {strconv.Itoa(runOptions.height)},
	}.Encode()

	client := &http.Client{}
	req, err := http.NewRequest("GET", fmt.Sprintf("%s?%s", host, formData), nil)
	if err != nil {
		logger.Panic(err)
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		logger.Panic(err)
	}

	defer resp.Body.Close()

	if resp.StatusCode > 299 {
		return
	}

	f, err := os.Create(path.Join(runOptions.outputDirectory, fileName))
	if err != nil {
		os.Remove(f.Name())
		logger.Panic(err)
	}

	defer f.Close()
	io.Copy(f, resp.Body)

	logger.Printf("saved file %s. completed in %s of which %d seconds is a delay", fileName, time.Since(start), runOptions.delay)
}

func checkServerAvailable(conf *config, logger *log.Logger) {
	pingPath := fmt.Sprintf("%s:%d/%s", conf.Server.Host, conf.Server.Port, conf.Server.PingPath)
	if _, err := http.Head(pingPath); err != nil {
		logger.Panicf("server %s is not available: %v", conf.Server.Host, err)
	}

	logger.Printf("screenshot taker server %s is available", conf.Server.Host)
}
