package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// awsProfile is the AWS SSO profile flowlog verifies before starting services.
// This is a placeholder: set it to the profile name in your own ~/.aws/config.
const awsProfile = "your-aws-profile"

// exportedCredentials mirrors the JSON emitted by
// `aws configure export-credentials --format process`.
type exportedCredentials struct {
	AccessKeyId     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
}

// checkAwsCredentials ports aws-credential-checker.ts: verify the awsProfile
// profile, offer SSO login if it is invalid, then export fresh credentials.
func checkAwsCredentials(debug bool) error {
	if debug {
		fmt.Printf("[debug] Checking AWS credentials for profile '%s'\n", awsProfile)
		fmt.Println("[debug] Credentials file: ~/.aws/credentials")
	}

	if credentialsFileValid(debug) {
		fmt.Println("AWS credentials OK")
		return nil
	}

	fmt.Fprintf(os.Stderr, "AWS credentials for '%s' in ~/.aws/credentials are expired or invalid.\n", awsProfile)

	if !promptYesNo(fmt.Sprintf("Run SSO login for profile '%s'? [Y/n] ", awsProfile)) {
		fmt.Fprintln(os.Stderr, "Continuing without valid AWS credentials.")
		return nil
	}

	if debug {
		fmt.Printf("[debug] Running: aws sso login --profile %s\n", awsProfile)
	}
	fmt.Println("Opening browser for SSO login...")
	login := exec.Command("aws", "sso", "login", "--profile", awsProfile)
	login.Stdin = os.Stdin
	login.Stdout = os.Stdout
	login.Stderr = os.Stderr
	if err := login.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "\nSSO login failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "Fix your AWS config and try again, or use --skip-aws-check to bypass.")
		fmt.Fprintln(os.Stderr)
		return err
	}
	if debug {
		fmt.Println("[debug] SSO login completed")
	}

	fmt.Println("Exporting credentials to ~/.aws/credentials...")
	creds, err := exportAwsCredentials(debug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nFailed to export credentials: %v\n", err)
		fmt.Fprintln(os.Stderr, "You may need to manually paste credentials into ~/.aws/credentials.")
		fmt.Fprintln(os.Stderr)
		return err
	}
	if err := writeAwsCredentialsFile(creds); err != nil {
		fmt.Fprintf(os.Stderr, "\nFailed to export credentials: %v\n", err)
		fmt.Fprintln(os.Stderr, "You may need to manually paste credentials into ~/.aws/credentials.")
		fmt.Fprintln(os.Stderr)
		return err
	}
	if debug {
		fmt.Printf("[debug] Wrote [%s] section to ~/.aws/credentials\n", awsProfile)
	}
	fmt.Printf("Credentials written to ~/.aws/credentials [%s]\n", awsProfile)
	return nil
}

func promptYesNo(message string) bool {
	fmt.Print(message)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.TrimSpace(line)
	return line == "" || strings.EqualFold(line, "y") || strings.EqualFold(line, "yes")
}

func credentialsFileValid(debug bool) bool {
	if debug {
		fmt.Printf("[debug] Running: AWS_CONFIG_FILE=/dev/null aws sts get-caller-identity --profile %s\n", awsProfile)
	}
	cmd := exec.Command("aws", "sts", "get-caller-identity", "--profile", awsProfile)
	cmd.Env = append(os.Environ(), "AWS_CONFIG_FILE=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		if debug {
			fmt.Printf("[debug] Failed: %s\n", commandErrorText(err))
		}
		return false
	}
	if debug {
		fmt.Printf("[debug] Result:\n%s\n", strings.TrimRight(string(out), "\n"))
	}
	return true
}

func exportAwsCredentials(debug bool) (exportedCredentials, error) {
	if debug {
		fmt.Printf("[debug] Running: aws configure export-credentials --profile %s --format process\n", awsProfile)
	}
	out, err := exec.Command("aws", "configure", "export-credentials", "--profile", awsProfile, "--format", "process").Output()
	if err != nil {
		return exportedCredentials{}, errors.New(commandErrorText(err))
	}
	var creds exportedCredentials
	if err := json.Unmarshal(out, &creds); err != nil {
		return exportedCredentials{}, err
	}
	if debug {
		short := creds.AccessKeyId
		if len(short) > 8 {
			short = short[:8]
		}
		fmt.Printf("[debug] Received credentials (AccessKeyId: %s...)\n", short)
	}
	return creds, nil
}

// commandErrorText mirrors execa's `err.stderr || err.message` fallback.
func commandErrorText(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return strings.TrimSpace(string(exitErr.Stderr))
	}
	return err.Error()
}

func writeAwsCredentialsFile(creds exportedCredentials) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return writeAwsCredentialsFileAt(filepath.Join(home, ".aws", "credentials"), creds)
}

func writeAwsCredentialsFileAt(path string, creds exportedCredentials) error {
	var content string
	if data, err := os.ReadFile(path); err == nil {
		content = string(data)
	} else if !os.IsNotExist(err) {
		return err
	}

	content = rewriteAwsCredentials(content, creds)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

// rewriteAwsCredentials returns content with any existing [awsProfile] section
// removed and a fresh one appended. It is the pure equivalent of the TS
// tool's `\[<profile>\][\s\S]*?(?=\n\[|$)` global regex replace; Go's RE2
// engine has no lookahead, so the search for the next section header is done
// by hand instead.
func rewriteAwsCredentials(content string, creds exportedCredentials) string {
	header := "[" + awsProfile + "]"
	for {
		start := strings.Index(content, header)
		if start == -1 {
			break
		}
		rest := content[start+len(header):]
		matchEnd := len(content)
		if end := strings.Index(rest, "\n["); end != -1 {
			matchEnd = start + len(header) + end
		}
		content = content[:start] + content[matchEnd:]
	}
	content = strings.TrimSpace(content)

	section := strings.Join([]string{
		header,
		"aws_access_key_id=" + creds.AccessKeyId,
		"aws_secret_access_key=" + creds.SecretAccessKey,
		"aws_session_token=" + creds.SessionToken,
	}, "\n")

	if content == "" {
		return section + "\n"
	}
	return content + "\n\n" + section + "\n"
}
