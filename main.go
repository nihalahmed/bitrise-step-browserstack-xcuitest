package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"time"
)

type BuildParams struct {
	AppUrl     string   `json:"app"`
	TestSuite  string   `json:"testSuite"`
	Devices    []string `json:"devices"`
	DeviceLogs bool     `json:"deviceLogs"`
}

func main() {
	username := getEnvVar("browserstack_username")
	password := getEnvVar("browserstack_password")
	ipaPath := getEnvVar("ipa_path")
	xcuitestPackagePath := getEnvVar("xcuitest_package_path")

	log.Printf("IPA path: %s", ipaPath)
	log.Printf("XCUITest package path: %s", xcuitestPackagePath)

	appUrl, err := uploadApp(ipaPath, username, password)
	if err != nil {
		log.Printf("Failed to upload app with error: %s", err)
		os.Exit(1)
	}

	testSuiteUrl, err := uploadTestSuite(xcuitestPackagePath, username, password)
	if err != nil {
		log.Printf("Failed to upload test suite with error: %s", err)
		os.Exit(1)
	}

	buildParams := &BuildParams{
		AppUrl:     appUrl,
		TestSuite:  testSuiteUrl,
		Devices:    []string{"iPhone XS-12"},
		DeviceLogs: true,
	}
	buildId, err := executeBuild(username, password, *buildParams)
	if err != nil {
		log.Printf("Failed to execute build with error: %s", err)
		os.Exit(1)
	}

	log.Printf("https://app-automate.browserstack.com/builds/%s", buildId)

	success, err := pollBuildStatus(username, password, buildId)
	if err != nil {
		log.Printf("Failed to poll build status with error: %s", err)
		os.Exit(1)
	}

	cmdLog, err := exec.Command("bitrise", "envman", "add", "--key", "BROWSERSTACK_BUILD_ID", "--value", buildId).CombinedOutput()
	if err != nil {
		log.Printf("Failed to expose output with envman, error: %s | output: %s", err, cmdLog)
		os.Exit(1)
	}

	if success {
		os.Exit(0)
	} else {
		log.Printf("Build failed")
		os.Exit(1)
	}
}

func getEnvVar(key string) string {
	value := os.Getenv(key)
	if value == "" {
		log.Printf("Environment variable %s not set", key)
		os.Exit(1)
	}
	return value
}

func uploadApp(path, username, password string) (string, error) {
	body, contentType, err := getMultiPartData(path)
	if err != nil {
		return "", err
	}
	response, err := makePostRequest("https://api-cloud.browserstack.com/app-automate/upload", username, password, *body, contentType)
	if err != nil {
		return "", err
	}
	if appUrl, ok := response["app_url"].(string); ok {
		return appUrl, nil
	} else {
		return "", errors.New("Key app_url not found in response")
	}
}

func uploadTestSuite(path, username, password string) (string, error) {
	body, contentType, err := getMultiPartData(path)
	if err != nil {
		return "", err
	}
	response, err := makePostRequest("https://api-cloud.browserstack.com/app-automate/xcuitest/test-suite", username, password, *body, contentType)
	if err != nil {
		return "", err
	}
	if testSuiteUrl, ok := response["test_url"].(string); ok {
		return testSuiteUrl, nil
	} else {
		return "", errors.New("Key test_url not found in response")
	}
}

func executeBuild(username, password string, buildParams BuildParams) (string, error) {
	body := new(bytes.Buffer)
	err := json.NewEncoder(body).Encode(buildParams)
	if err != nil {
		return "", err
	}
	response, err := makePostRequest("https://api-cloud.browserstack.com/app-automate/xcuitest/build", username, password, *body, "application/json")
	if err != nil {
		return "", err
	}
	if buildId, ok := response["build_id"].(string); ok {
		return buildId, nil
	} else {
		return "", errors.New("Key build_id not found in response")
	}
}

func pollBuildStatus(username, password, buildId string) (bool, error) {
	c := time.Tick(30 * time.Second)
	for _ = range c {
		response, err := makeGetRequest(fmt.Sprintf("https://api-cloud.browserstack.com/app-automate/xcuitest/builds/%s", buildId), username, password)
		if err != nil {
			return false, err
		}
		if status, ok := response["status"].(string); ok {
			if status == "done" {
				return true, nil
			} else if status == "failed" {
				return false, nil
			} else if status == "running" {
				continue
			} else {
				return false, errors.New(fmt.Sprintf("Unsupported status value %s found in response", status))
			}
		} else {
			return false, errors.New("Key status not found in response")
		}
	}
	return false, errors.New("Polling exited without returning status")
}

func makeGetRequest(url string, username string, password string) (map[string]interface{}, error) {
	return makeRequest("GET", url, username, password, bytes.Buffer{}, "")
}

func makePostRequest(url string, username string, password string, body bytes.Buffer, contentType string) (map[string]interface{}, error) {
	return makeRequest("POST", url, username, password, body, contentType)
}

func makeRequest(method, url, username, password string, body bytes.Buffer, contentType string) (map[string]interface{}, error) {
	var result map[string]interface{}

	req, err := http.NewRequest(method, url, &body)
	if err != nil {
		return result, err
	}
	req.SetBasicAuth(username, password)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	log.Printf("%s request: %s", method, url)

	client := &http.Client{}
	res, err := client.Do(req)
	if err != nil {
		return result, err
	}

	log.Println("Response status code:", res.StatusCode)

	err = json.NewDecoder(res.Body).Decode(&result)
	if err != nil {
		return result, err
	}

	log.Println("Response:", result)

	if res.StatusCode >= 200 && res.StatusCode <= 299 {
		return result, nil
	} else {
		return result, errors.New(fmt.Sprintf("HTTP status code %d not in the 2xx range", res.StatusCode))
	}
}

func getMultiPartData(path string) (*bytes.Buffer, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()

	var buffer bytes.Buffer
	multiPartWriter := multipart.NewWriter(&buffer)

	fileWriter, err := multiPartWriter.CreateFormFile("file", "file")
	if err != nil {
		return nil, "", err
	}

	_, err = io.Copy(fileWriter, file)
	if err != nil {
		return nil, "", err
	}

	multiPartWriter.Close()

	return &buffer, multiPartWriter.FormDataContentType(), nil
}
