package main

import (
	"bytes"
	b64 "encoding/base64"
	"encoding/xml"
	"errors"
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

	httpClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Sometimes we use the JSessionID (to act like a browser)
	// Other times we user an HTTP Basic authorization header (to use the REST API).
	// The choice is based on which is easier and/or arbitrary historical choices.
	jSessionId := logInToBamboo(bambooUrl, username, password, httpClient)
	authHeader := buildAuthorizationHeader(username, password)

	handleAllBuilds(bambooUrl, jSessionId, authHeader, httpClient)
}

func buildAuthorizationHeader(username string, password string) string {
	return "Basic " + b64.StdEncoding.EncodeToString([]byte(username+":"+password))
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

func handleAllBuilds(bambooUrl string, jSessionId string, authHeader string, httpClient *http.Client) {
	scanStartTime := time.Now()
	fmt.Println("Starting scan at ", scanStartTime)

	counts := make(map[string]int)
	counts["scanned"] = 0
	counts["skipped"] = 0
	counts["commented"] = 0

	maxResults := 100
	atomUrl := fmt.Sprintf("%s/plugins/servlet/streams?local=true&maxResults=%d", bambooUrl, maxResults)
	req, err := http.NewRequest("GET", atomUrl, nil)
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
	atomFeedParser := gofeed.NewParser()
	feed, err := atomFeedParser.ParseString(string(body))
	if err != nil {
		panic(err)
	}
	// Sort by date, so most recent failure comes first
	items := feed.Items
	sort.Slice(items, func(i, j int) bool {
		return items[i].PublishedParsed.Format(time.RFC3339) > items[j].PublishedParsed.Format(time.RFC3339)
	})
	maxHoursSincePublish := -1.0
	minHoursSincePublish := 9999.0

	planNameToLastGoodCommit := make(map[string]string)
	branchNamesToLastGoodCommits := make(map[string]string)

	for _, item := range items {
		fmt.Println()
		if num, ok := counts["scanned"]; ok {
			counts["scanned"] = num + 1
		}

		link := item.Link
		publishedTime := item.PublishedParsed.Format(time.RFC3339)
		fmt.Print(link, " : ")

		skipScan := false
		isSuccess := false

		// Keep only failures, which have a category of "build.failed"
		for _, category := range item.Categories {
			if category == "build.successful" {
				fmt.Print("Skipping: Successful build ... ")
				skipScan = true
				isSuccess = true
			}
		}

		splitBySlash := strings.Split(link, "/")
		buildId := splitBySlash[len(splitBySlash)-1] // Ex: CRAB-CWS144-JOB1-33

		splitByHyphen := strings.Split(buildId, "-")
		if len(splitByHyphen) != 4 {
			panic("Unexpected format of build ID: " + buildId)
		}
		buildNumber := splitByHyphen[3]

		// According to the REST API, "buildKey" usually refers to CWS144 in the example above.
		// But in other contexts (URL query parameters) it's CRAB-CWS144.
		buildKey := strings.Join(splitByHyphen[0:2], "-")

		// Read the existing labels on this build to find out if we've already processed it
		labels := getLabels(bambooUrl, buildKey, buildNumber, jSessionId, httpClient)

		for _, label := range labels {
			if label == "bambot-scanned" {
				fmt.Print("Skipping: Bambot already scanned ... ")
				skipScan = true
			} else if strings.HasPrefix(label, "crab-") {
				fmt.Print("Skipping: Already manually labeled ... ")
				skipScan = true
			}
		}

		if strings.Contains(item.Content, "tests failed") {
			// Skip this build, if Bamboo was able to parse the test failures we don't have any value to add
			fmt.Print("Skipping: Bamboo found test failures ... ")
			skipScan = true
		}

		timeSincePublish := time.Now().Sub(*item.PublishedParsed)
		hoursSincePublish := timeSincePublish.Hours()
		if hoursSincePublish > maxHoursSincePublish {
			maxHoursSincePublish = hoursSincePublish
		}
		if hoursSincePublish < minHoursSincePublish {
			minHoursSincePublish = hoursSincePublish
		}
		if hoursSincePublish > 24*7 {
			fmt.Print("Skipping: too old:", publishedTime, "...")
			skipScan = true
		}

		if isSuccess {
			result := getBuildResult(bambooUrl, buildKey, buildNumber, authHeader, httpClient)
			if strings.Contains(buildKey, "CRAB-CWO") &&
				result.BuildState == "Successful" {
				// Consider only the most recent successful build on each branch
				if _, present := planNameToLastGoodCommit[result.PlanName]; !present {
					planNameToLastGoodCommit[result.PlanName] = result.VcsRevisionKey
					if branchName, err := branchNameFromPlanName(result.PlanName); err == nil {
						branchNamesToLastGoodCommits[branchName] = result.VcsRevisionKey
					}
				}
			}
		}

		if skipScan {
			if num, ok := counts["skipped"]; ok {
				counts["skipped"] = num + 1
			}
			continue
		}

		scanResult := scanBuild(bambooUrl, buildKey, buildNumber, jSessionId, httpClient)

		if scanResult.Comment != "" {
			if num, ok := counts["commented"]; ok {
				counts["commented"] = num + 1
			}

			commentContent := ""
			commentContent += scanResult.Comment + "\n\n"
			if scanResult.JiraIssueId != "" {
				commentContent += "This is a known issue in JIRA: " + scanResult.JiraIssueId + "\n\n"
			}
			commentContent += "Log snippet:\n" + scanResult.LogSnippet

			print("Adding comment & 'bambot-scanned' label")
			addCommentWithApi(bambooUrl, buildKey, buildNumber, commentContent, authHeader, httpClient)
			addLabel(bambooUrl, buildKey, buildNumber, "bambot-scanned", jSessionId, httpClient)
		} else {
			print("Couldn't find cause of failure")
		}
	}

	branchNamesToLastGoodCommitsString := mapToText(branchNamesToLastGoodCommits)
	writeStringToFile("branchNamesToLastGoodCommits.txt", branchNamesToLastGoodCommitsString)

	elapsed := time.Since(scanStartTime)
	fmt.Println("\nFinished scan at ", time.Now())
	fmt.Println("Stats: ", "scanned =", counts["scanned"], ", skipped =", counts["skipped"], ", commented =", counts["commented"])
	fmt.Println("Oldest build was ", maxHoursSincePublish, " hours ago; youngest build was ", minHoursSincePublish, " hours ago")
	fmt.Println("It took ", elapsed, " to run the scan")
	fmt.Println("Branch names to commits:\n", branchNamesToLastGoodCommitsString)
}

func mapToText(theMap map[string]string) string {
	var result strings.Builder
	for key, value := range theMap {
		result.WriteString(key)
		result.WriteString(" ")
		result.WriteString(value)
		result.WriteString("\n")
	}
	return result.String()
}

func writeStringToFile(fileName string, text string) {
	err := ioutil.WriteFile(fileName, []byte(text), 0644)
	if err != nil {
		panic(err)
	}
}

func branchNameFromPlanName(planName string) (string, error) {
	if planName == "Windows Official" {
		return "develop", nil
	} else if strings.HasPrefix(planName, "release-") {
		return strings.Replace(planName, "release-", "release/", 1), nil
	} else {
		return "", errors.New("only release branches are supported for tagging")
	}
}

type BambooResult struct {
	XMLName        xml.Name `xml:"result"`
	PlanName       string   `xml:"planName"`
	VcsRevisionKey string   `xml:"vcsRevisionKey"`
	BuildState     string   `xml:"buildState"`
}

func getBuildResult(bambooUrl string, buildKey string, buildNumber string, authHeader string, httpClient *http.Client) BambooResult {
	getDetailsUrl := bambooUrl + "/rest/api/latest/result/" + buildKey + "/" + buildNumber + "?expand=artifacts&expand=changes&expand=results.result.artifacts&expand=results.result.labels&expand=results.result.comments&expand=results.result.jiraIssues&expand=changes.change&expand=changes.change.files&expand=metadata"
	req, err := http.NewRequest("GET", getDetailsUrl, nil)
	if err != nil {
		panic(err)
	}
	req.Header.Add("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/xml")
	resp, err := httpClient.Do(req)
	if err != nil {
		panic(err)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}

	var parsedResult BambooResult
	err = xml.Unmarshal(body, &parsedResult)
	if err != nil {
		panic(err)
	}

	err = resp.Body.Close()
	if err != nil {
		panic(err)
	}
	return parsedResult
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

func addCommentWithApi(bambooUrl string, buildKey string, buildNumber string, commentContent string, authHeader string, httpClient *http.Client) {
	addCommentUrl := bambooUrl + "/rest/api/latest/result/" + buildKey + "-" + buildNumber + "/comment?os_authType=basic"
	reqBody := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
	<comment>
		<content>` + escapeXmlString(commentContent) + `</content>
	</comment>`
	req, err := http.NewRequest("POST", addCommentUrl, strings.NewReader(reqBody))
	if err != nil {
		panic(err)
	}
	req.Header.Add("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/xml")
	resp, err := httpClient.Do(req)
	if err != nil {
		panic(err)
	}
	println("Sent a comment with length ", len(commentContent))
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	bodyStr := string(body)
	err = resp.Body.Close()
	println("After posting a comment, status was: ", resp.Status, ", body was: ", bodyStr)
	if err != nil {
		panic(err)
	}
	return
}

func escapeXmlString(s string) string {
	b := new(bytes.Buffer)
	err := xml.EscapeText(b, []byte(s))
	if err != nil {
		panic(err)
	}
	return b.String()
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
		fmt.Print("Failed to download logs from", downloadLogsUrl)
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

	return scanString(bodyStr)
}

// Given a log file, determine if it matches one of the known patterns for build failures
func scanString(bodyStr string) ScanResult {
	start := "[ERROR] COMPILATION ERROR"
	end := "[INFO] ------------------------------------------------------------------------"
	context := getSubstring(bodyStr, start, end)
	if len(context) > 0 {
		return ScanResult{Comment: "Bambot detected a Java compilation error!", LogSnippet: context}
	}

	start = "ERROR: Coverage for"
	end = "with result: Failed"
	context = getSubstring(bodyStr, start, end)
	if len(context) > 0 {
		return ScanResult{Comment: "Bambot detected a Javascript coverage error!", LogSnippet: context}
	}

	// C# build logs seem to spread the error details across a large number of lines.
	// So, we have two patterns to try to catch the areas of interest.
	start = "Errors and Failures:"
	end = "Error(s)"
	context = getSubstring(bodyStr, start, end)
	if len(context) > 0 {
		return ScanResult{Comment: "Bambot detected a C# build error!", LogSnippet: context}
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

	start = "***** ERROR *****"
	end = "with result: Failed"
	context = getSubstring(bodyStr, start, end)
	if len(context) > 0 {
		return ScanResult{Comment: "Bambot detected an error!", LogSnippet: context}
	}

	start = "=================================== FAILURES ==================================="
	end = "with result: Failed"
	context = getSubstring(bodyStr, start, end)
	if len(context) > 0 {
		return ScanResult{Comment: "Bambot detected a Python pytest error!", LogSnippet: context}
	}

	start = "Seeq Build Step: Building with Grunt"
	end = "Aborted due to warnings."
	context = getSubstring(bodyStr, start, end)
	if len(context) > 0 {
		return ScanResult{Comment: "Bambot detected a front-end Grunt build error!", LogSnippet: context}
	}

	start = "Errors and Failures:"
	end = "Committing..."
	context = getSubstring(bodyStr, start, end)
	if len(context) > 0 {
		return ScanResult{Comment: "Bambot detected a C# unit test/integration test failure!", LogSnippet: context}
	}

	return nonMatch()
}

// Get a portion of a string based on a start pattern and end pattern. The result will include both start & end.
// If no result is found, an empty string will be returned.
func getSubstring(input string, start string, end string) string {
	endIndex := strings.LastIndex(input, end)
	if endIndex >= 0 {
		beforeEnd := input[:endIndex]
		startIndex := strings.LastIndex(beforeEnd, start)

		if startIndex >= 0 {
			fullSnippet := beforeEnd[startIndex:]

			// In case the snippet is huge in either dimension, truncate it
			snippet := truncateLines(fullSnippet, 160, 2000)
			return snippet
		}
	}
	return ""
}

// Given a multi-line string, truncate each line to be no wider than maxWidth,
// adding an ellipsis (...) any place that is truncated.
// Then, limit the string to at most maxLines lines,
// adding an ellipsis (...) at the end if the lines were truncated.
func truncateLines(bodyStr string, maxWidth int, maxLines int) string {
	lines := strings.Split(bodyStr, "\n")
	var tooManyLines = false
	if maxLines >= 0 && len(lines) > maxLines {
		lines = lines[0:maxLines]
		tooManyLines = true
	}

	var result strings.Builder
	for idx, line := range lines {
		if len(line) > maxWidth {
			result.WriteString(line[0 : maxWidth-3])
			result.WriteString("...")
		} else {
			result.WriteString(line)
		}
		if idx < len(lines)-1 {
			result.WriteString("\n")
		}
	}
	if tooManyLines {
		result.WriteString("\n...")
	}
	return result.String()
}
