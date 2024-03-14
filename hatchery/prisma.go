package hatchery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
)

type Token struct {
	Token string `json:"token"`
}

type InstallBundle struct {
	WsAddress string `json:"wsAddress"`
	Bundle    string `json:"installBundle"`
}

func getPrismaToken(username string, password string) (*string, error) {
	postBody, _ := json.Marshal(map[string]string{
		"username": username,
		"password": password,
	})
	reqBody := bytes.NewBuffer(postBody)
	authEndpoint := Config.Config.PrismaConfig.ConsoleAddress + "/api/v1/authenticate"
	resp, err := http.Post(authEndpoint, "application/json", reqBody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		Config.Logger.Print(string(b))
		return nil, errors.New("Error authenticating with Prisma Cloud: " + string(b))
	}
	//We Read the response body on the line below.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result Token
	if err := json.Unmarshal(body, &result); err != nil {
		fmt.Println("Invalid response from prisma auth endpoint: " + authEndpoint)
	}

	return &result.Token, nil
}

func getInstallBundle() (*InstallBundle, error) {
	username := os.Getenv("PRISMA_ACCESS_KEY_ID")
	password := os.Getenv("PRISMA_SECRET_KEY")
	token, err := getPrismaToken(username, password)
	if err != nil {
		return nil, err
	}

	installBundleEndpoint := Config.Config.PrismaConfig.ConsoleAddress + fmt.Sprintf("/api/%s/defenders/install-bundle?consoleaddr=", Config.Config.PrismaConfig.ConsoleVersion) + Config.Config.PrismaConfig.ConsoleAddress + "&defenderType=appEmbedded"
	var bearer = "Bearer " + *token
	// Create a new request using http
	req, err := http.NewRequest("GET", installBundleEndpoint, nil)
	if err != nil {
		return nil, err
	}
	// add authorization header to the req
	req.Header.Add("Authorization", bearer)

	// Send req using http Client
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		Config.Logger.Print(string(b))
		return nil, errors.New("Error getting install bundle: " + string(b))
	}
	//We Read the response body on the line below.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var result InstallBundle
	if err := json.Unmarshal(body, &result); err != nil {
		fmt.Println("Invalid response from prisma install_bundle endpoint: " + installBundleEndpoint)
	}
	return &result, nil
}

func getPrismaImage() (*string, error) {
	username := os.Getenv("PRISMA_ACCESS_KEY_ID")
	password := os.Getenv("PRISMA_SECRET_KEY")
	token, err := getPrismaToken(username, password)
	if err != nil {
		return nil, err
	}

	imageEndpoint := Config.Config.PrismaConfig.ConsoleAddress + fmt.Sprintf("/api/%s/defenders/image-name", Config.Config.PrismaConfig.ConsoleVersion)
	var bearer = "Bearer " + *token
	// Create a new request using http
	req, err := http.NewRequest("GET", imageEndpoint, nil)
	if err != nil {
		return nil, err
	}
	// add authorization header to the req
	req.Header.Add("Authorization", bearer)

	// Send req using http Client
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		Config.Logger.Print(string(b))
		return nil, errors.New("Error getting install bundle: " + string(b))
	}
	//We Read the response body on the line below.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	sb, err := strconv.Unquote(string(body))
	if err != nil {
		return nil, err
	}
	return &sb, nil
}
