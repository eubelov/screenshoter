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
	conf := readConfig()
	log.Printf("%+v", *conf)

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

	fmt.Printf("%+v\n", opt)
	checkServerAvailable(opt.server)
	takeScreenshots(opt)
}

func readConfig() *config {
	f, err := os.Open("config.yaml")
	if err != nil {
		log.Panicf("config.yaml not found in binary directory: %v", err)
	}

	defer f.Close()
	var conf config
	dec := yaml.NewDecoder(f)
	if err = dec.Decode(&conf); err != nil {
		log.Panicf("can't parse config.yaml: %v", err)
	}

	return &conf
}

func takeScreenshots(runOptions *runOptions) {
	if file, err := os.Open(runOptions.inputFilePath); err != nil {
		log.Panicf("file does not exist: %s", runOptions.inputFilePath)
	} else {
		defer file.Close()

		scanner := bufio.NewScanner(file)
		actionURL := fmt.Sprintf("%s:%d/%s", runOptions.server.Server.Host, runOptions.server.Server.Port, runOptions.server.Server.ActionPath)

		for scanner.Scan() {
			if err := runOptions.sem.Acquire(ctx, 1); err != nil {
				log.Printf("failed to acquire semaphore: %v", err)
			}

			url := scanner.Text()

			go func() {
				saveImage(runOptions, actionURL, url)
				runOptions.sem.Release(1)
			}()
		}

		if err := runOptions.sem.Acquire(ctx, int64(*concurrency)); err != nil {
			log.Printf("failed to acquire semaphore: %v", err)
		}
	}
}

func saveImage(runOptions *runOptions, host, u string) {
	log.Printf("processing %s", u)

	var fileName string

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
		log.Panic(err)
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Panic(err)
	}

	defer resp.Body.Close()

	log.Printf("response status:%s", resp.Status)
	if resp.StatusCode > 299 {
		return
	}

	if f, err := os.Create(path.Join(runOptions.outputDirectory, fileName)); err != nil {
		os.Remove(f.Name())
		log.Panic(err)
	} else {
		defer f.Close()
		io.Copy(f, resp.Body)
	}
}

func checkServerAvailable(conf *config) {
	pingPath := fmt.Sprintf("%s:%d/%s", conf.Server.Host, conf.Server.Port, conf.Server.PingPath)
	if _, err := http.Head(pingPath); err != nil {
		log.Panicf("server %s is not available: %v", conf.Server.Host, err)
	}

	log.Printf("screenshot taker server %s is available", conf.Server.Host)
}
