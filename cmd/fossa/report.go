package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/briandowns/spinner"
	logging "github.com/op/go-logging"
	"github.com/urfave/cli"

	"github.com/fossas/fossa-cli/config"
	"github.com/fossas/fossa-cli/module"
)

var reportLogger = logging.MustGetLogger("report")

type dependencyResponse struct {
	Loc struct {
		Package  string
		Revision string
	}
	Licenses []licenseResponse
	Project  struct {
		Title   string
		URL     string
		Authors []string
	}
}

type licenseResponse struct {
	ID       string `json:"spdx_id"`
	Title    string
	FullText string
}

func getRevisions(apiURL string, apiKey string, locators []string) ([]dependencyResponse, error) {
	qs := strings.Join(locators, "&")

	res, err := makeAPIRequest(http.MethodGet, apiURL+"?"+qs, apiKey, nil)
	if err != nil {
		return nil, fmt.Errorf("Could not get licenses from FOSSA API: %s", err.Error())
	}
	var deps []dependencyResponse
	err = json.Unmarshal(res, &deps)
	if err != nil {
		return nil, fmt.Errorf("Could not parse API response: %s", err.Error())
	}
	return deps, nil
}

func reportLicenses(s *spinner.Spinner, endpoint, apiKey string, a analysis) {
	server, err := url.Parse(endpoint)
	if err != nil {
		reportLogger.Fatalf("Invalid FOSSA endpoint: %s", err.Error())
	}
	api, err := server.Parse(fmt.Sprintf("/api/revisions"))
	if err != nil {
		reportLogger.Fatalf("Invalid API endpoint: %s", err.Error())
	}

	s.Suffix = " Loading licenses..."
	s.Start()
	total := 0
	for _, deps := range a {
		for _ = range deps {
			total++
		}
	}
	var locators []string
	var responses []dependencyResponse
	for _, deps := range a {
		for _, dep := range deps {
			if dep.Revision() != "" {
				locator := string(module.MakeLocator(dep))
				if dep.Fetcher() == "go" {
					locator = config.MakeLocator("git", dep.Package(), dep.Revision())
				}
				locators = append(locators, fmt.Sprintf("locator[%d]=%s", len(locators), url.QueryEscape(locator)))

				// We batch these in groups of 20 for pagination/performance purposes.
				// TODO: do this in parallel.
				if len(locators) == 20 {
					responsePage, err := getRevisions(api.String(), apiKey, locators)
					if err != nil {
						s.Stop()
						reportLogger.Fatalf("Could load licenses: %s", err.Error())
					}
					responses = append(responses, responsePage...)
					locators = []string{}
					s.Stop()
					s.Suffix = fmt.Sprintf(" Loading licenses (%d/%d done)...", len(responses), total)
					s.Restart()
				}
			}
		}
	}
	if len(locators) > 0 {
		responsePage, err := getRevisions(api.String(), apiKey, locators)
		if err != nil {
			s.Stop()
			reportLogger.Fatalf("Could load licenses: %s", err.Error())
		}
		responses = append(responses, responsePage...)
	}
	s.Stop()

	depsByLicense := make(map[licenseResponse][]dependencyResponse)
	for _, dep := range responses {
		for _, license := range dep.Licenses {
			depsByLicense[license] = append(depsByLicense[license], dep)
		}
	}

	fmt.Printf("This software includes the following software and licenses:\n\n")
	for license, deps := range depsByLicense {
		fmt.Printf(`
========================================================================
%s
========================================================================

The following software have components provided under the terms of this license:

`, license.Title)
		for _, dep := range deps {
			fmt.Printf("- %s (from %s)\n", dep.Project.Title, dep.Project.URL)
		}
	}
	fmt.Println()
}

func reportCmd(c *cli.Context) {
	conf, err := config.New(c)
	if err != nil {
		reportLogger.Fatalf("Could not load configuration: %s", err.Error())
	}

	s := spinner.New(spinner.CharSets[11], 100*time.Millisecond)

	s.Suffix = " Analyzing modules..."
	s.Start()
	analysis, err := doAnalyze(conf.Modules, conf.AnalyzeCmd.AllowUnresolved)
	s.Stop()
	if err != nil {
		reportLogger.Fatalf("Could not complete analysis (is the project built?): %s", err.Error())
	}

	switch conf.ReportCmd.Type {
	case "licenses":
		reportLicenses(s, conf.Endpoint, conf.APIKey, analysis)
	case "dependencies":
		outMap := make(map[string][]module.Dependency)
		for key, deps := range analysis {
			outMap[key.module.Name] = deps
		}
		out, err := json.Marshal(outMap)
		if err != nil {
			reportLogger.Fatalf("Could not marshal analysis: %s", err.Error())
		}
		fmt.Println(string(out))
	default:
		reportLogger.Fatalf("Report type is not recognized (supported types are \"dependencies\" or \"licenses\": %s", conf.ReportCmd.Type)
	}
}
