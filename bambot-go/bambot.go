package main

import (
	"fmt"
	"github.com/mmcdole/gofeed"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

func main() {
	// PARAMETERS
	username, exists := os.LookupEnv("BAMBOO_USERNAME")
	if !exists {
		panic("Missing BAMBOO_USERNAME environment variable")
	}
	password, exists := os.LookupEnv("BAMBOO_PASSWORD")
	if !exists {
		panic("Missing BAMBOO_PASSWORD environment variable")
	}
	bambooUrl, exists := os.LookupEnv("BAMBOO_URL")
	if !exists {
		panic("Missing BAMBOO_URL environment variable")
	}

	buildPlans := []string{
		"CRAB-CUCS", "CRAB-UINST", "CRAB-CUO", "CRAB-CUS", "CRAB-CUD", "CRAB-CUU", "CRAB-CWCS", "CRAB-WINST",
		"CRAB-WNL", "CRAB-CWO", "CRAB-SLOW", "CRAB-CWDS", "CRAB-CWS", "CRAB-CRABDEV", "CRAB-CWU", "CRAB-CWWS"}

	httpClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	jSessionId := logInToBamboo(bambooUrl, username, password, httpClient)

	for _, buildPlan := range buildPlans {
		handleBuildPlan(bambooUrl, buildPlan, jSessionId, httpClient)
	}
}

func logInToBamboo(bambooUrl string, username string, password string, httpClient *http.Client) string {
	loginUrl := bambooUrl + "/userlogin.action"

	reqBody := strings.NewReader(`os_destination=%2Fstart.action&os_username=` + username + `&os_password=` + password)
	req, err := http.NewRequest("POST", loginUrl, reqBody)
	if err != nil {
		panic(err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		panic(err)
	}

	// We expect a status code of 302, and a redirect to /start.action
	if resp.StatusCode == 302 {
		url, err := resp.Location()
		if err != nil {
			panic(err)
		}
		if url.String() == bambooUrl+"/start.action" {
			fmt.Println("Successful login!")
		} else {
			panic("Failed login, redirected to " + url.String())
		}
	} else {
		panic("Failed login, response code was " + string(resp.StatusCode) + ", but we expect a response of 302")
	}
	_ = resp.Body.Close()

	// Extract the JSESSIONID cookie, which we can use for future authenticated requests
	setCookieHeader := resp.Header.Get("Set-Cookie")
	re := regexp.MustCompile("JSESSIONID=([0-9A-Z]+).*")
	jSessionId := re.FindStringSubmatch(setCookieHeader)[1]
	return jSessionId
}

func handleBuildPlan(bambooUrl string, buildKey string, jSessionId string, httpClient *http.Client) {
	rssUrl := fmt.Sprintf("%s/rss/createAllBuildsRssFeed.action?feedType=rssFailed&buildKey=%s", bambooUrl, buildKey)
	req, err := http.NewRequest("GET", rssUrl, nil)
	if err != nil {
		panic(err)
	}
	req.Header.Add("Cookie", "JSESSIONID="+jSessionId)
	resp, err := httpClient.Do(req)
	if err != nil {
		panic(err)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	err = resp.Body.Close()
	if err != nil {
		panic(err)
	}
	rssFeedParser := gofeed.NewParser()
	feed, err := rssFeedParser.ParseString(string(body))
	if err != nil {
		panic(err)
	}
	// Sort by date, so most recent failure comes first
	items := feed.Items
	sort.Slice(items, func(i, j int) bool {
		return items[i].PublishedParsed.Format(time.RFC3339) > items[j].PublishedParsed.Format(time.RFC3339)
	})
	for _, item := range items {
		publishedTime := item.PublishedParsed.Format(time.RFC3339)
		fmt.Printf("\n%s: %s %s\n", publishedTime, item.Title, item.Link)

		re := regexp.MustCompile(buildKey + "-([0-9]+).*")
		buildNumber := re.FindStringSubmatch(item.Title)[1]

		// Read the existing labels on this build to find out if we've already processed it
		labels := getLabels(bambooUrl, buildKey, buildNumber, jSessionId, httpClient)
		fmt.Printf("Found labels: %v\n", labels)

		skipScan := false
		for _, label := range labels {
			if label == "bambot-scanned" {
				fmt.Println("Skipping scan of build " + item.Link + ": Bambot already scanned")
				skipScan = true
			} else if strings.HasPrefix(label, "crab-") {
				fmt.Println("Skipping scan of build " + item.Link + ": Already manually labeled")
				skipScan = true
			}
		}

		if strings.Contains(item.Title, "tests failed") {
			// Skip this build, if Bamboo was able to parse the test failures we don't have any value to add
			fmt.Println("Skipping scan of build " + item.Link + ": Bamboo was able to parse the test failures")
			skipScan = true
		}

		timeSincePublish := time.Now().Sub(*item.PublishedParsed)
		if timeSincePublish.Hours() > 24*7 {
			fmt.Println("Skipping scan of build " + item.Link + ": too old, publish time " + publishedTime)
			skipScan = true
		}

		if skipScan {
			continue
		}

		scanResult := scanBuild(bambooUrl, buildKey, buildNumber, jSessionId, httpClient)

		if scanResult.Comment != "" {
			commentContent := ""
			commentContent += scanResult.Comment + "\n\n"
			if scanResult.JiraIssueId != "" {
				commentContent += "This is a known issue in JIRA: " + scanResult.JiraIssueId + "\n\n"
			}
			commentContent += "Log snippet:\n" + scanResult.LogSnippet

			addComment(bambooUrl, buildKey, buildNumber, commentContent, jSessionId, httpClient)
			addLabel(bambooUrl, buildKey, buildNumber, "bambot-scanned", jSessionId, httpClient)
		}
	}
}

// Get the Bamboo labels on a build
func getLabels(bambooUrl string, buildKey string, buildNumber string, jSessionId string, httpClient *http.Client) []string {
	addLabelsUrl := bambooUrl + "/build/label/ajax/editLabels.action?buildNumber=" + buildNumber + "&buildKey=" + buildKey
	req, err := http.NewRequest("GET", addLabelsUrl, nil)
	if err != nil {
		panic(err)
	}
	req.Header.Add("Cookie", "JSESSIONID="+jSessionId)
	resp, err := httpClient.Do(req)
	if err != nil {
		panic(err)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	bodyStr := string(body)
	err = resp.Body.Close()
	if err != nil {
		panic(err)
	}

	re := regexp.MustCompile("data-label=\"([a-z0-9-]+)\"")
	matches := re.FindAllStringSubmatch(bodyStr, -1)

	var labels []string
	for _, match := range matches {
		labels = append(labels, match[1])
	}
	return labels
}

// Add a Bamboo label to a build
func addLabel(bambooUrl string, buildKey string, buildNumber string, label string, jSessionId string, httpClient *http.Client) []string {
	addLabelsUrl := bambooUrl + "/build/label/ajax/addLabels.action"
	reqBody := strings.NewReader(`buildKey=` + buildKey + `&buildNumber=` + buildNumber + `&labelInput=` + label)
	req, err := http.NewRequest("POST", addLabelsUrl, reqBody)
	if err != nil {
		panic(err)
	}
	req.Header.Add("Cookie", "JSESSIONID="+jSessionId)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		panic(err)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	bodyStr := string(body)
	err = resp.Body.Close()
	if err != nil {
		panic(err)
	}

	re := regexp.MustCompile("data-label=\"([a-z0-9-]+)\"")
	matches := re.FindAllStringSubmatch(bodyStr, -1)

	var labels []string
	for _, match := range matches {
		labels = append(labels, match[1])
	}
	return labels
}

func addComment(bambooUrl string, buildKey string, buildNumber string, commentContent string, jSessionId string, httpClient *http.Client) {
	addCommentUrl := bambooUrl + "/build/ajax/createComment.action"
	reqBody := strings.NewReader(`buildKey=` + buildKey + `&buildNumber=` + buildNumber + `&commentContent=` + commentContent)
	req, err := http.NewRequest("POST", addCommentUrl, reqBody)
	if err != nil {
		panic(err)
	}
	req.Header.Add("Cookie", "JSESSIONID="+jSessionId)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		panic(err)
	}
	err = resp.Body.Close()
	if err != nil {
		panic(err)
	}
	return
}

type ScanResult struct {
	Comment     string
	LogSnippet  string
	JiraIssueId string
}

func nonMatch() ScanResult {
	return ScanResult{Comment: "", LogSnippet: "", JiraIssueId: ""}
}

// Investigate a build -- if it failed and the cause could be identified, return information about it!
func scanBuild(bambooUrl string, buildKey string, buildNumber string, jSessionId string, httpClient *http.Client) ScanResult {
	downloadLogsUrl := bambooUrl + "/download/" + buildKey + "-JOB1/build_logs/" + buildKey + "-JOB1-" + buildNumber + ".log?disposition=attachment"
	fmt.Println("downloadLogsUrl is ", downloadLogsUrl)

	// Download the logs!
	req, err := http.NewRequest("GET", downloadLogsUrl, nil)
	if err != nil {
		panic(err)
	}

	req.Header.Add("Cookie", "JSESSIONID="+jSessionId)
	resp, err := httpClient.Do(req)
	if err != nil {
		panic(err)
	}
	if resp.StatusCode != 200 {
		fmt.Println("Failed to download logs for build " + buildKey + "-" + buildNumber + ", tried URL " + downloadLogsUrl)
		return nonMatch()
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	err = resp.Body.Close()
	if err != nil {
		panic(err)
	}

	bodyStr := string(body)

	// Now, try to figure out the cause of the build failure!
	start := "[ERROR] COMPILATION ERROR"
	end := "[INFO] ------------------------------------------------------------------------"
	context := getSubstring(bodyStr, start, end)
	if len(context) > 0 {
		return ScanResult{Comment: "Bambot detected a Java compilation error!", LogSnippet: context}
	}

	start = "Build FAILED."
	end = "Error(s)"
	context = getSubstring(bodyStr, start, end)
	if len(context) > 0 {
		return ScanResult{Comment: "Bambot detected a C# build failure!", LogSnippet: context}
	}

	start = "[WARNING] Rule violated for bundle"
	end = "Coverage checks have not been met. See log for details."
	context = getSubstring(bodyStr, start, end)
	if len(context) > 0 {
		return ScanResult{Comment: "Bambot detected Java code coverage was below the required threshold!", LogSnippet: context}
	}

	start = "[INFO] BUILD FAILURE"
	end = "with result: Failed"
	context = getSubstring(bodyStr, start, end)
	if len(context) > 0 {
		return ScanResult{Comment: "Bambot detected a Maven (Java build system) error!", LogSnippet: context}
	}

	return nonMatch()
}

// Get a portion of a string based on a start pattern and end pattern. The result will include both start & end.
// If no result is found, an empty string will be returned.
func getSubstring(input string, start string, end string) string {
	startIndex := strings.Index(input, start)
	if startIndex >= 0 {
		afterStart := input[startIndex:]
		endIndex := strings.Index(afterStart, end)

		if endIndex >= 0 {
			return afterStart[0 : endIndex+len(end)]
		}
	}
	return ""
}
