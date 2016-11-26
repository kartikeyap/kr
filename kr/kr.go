package main

/*
* CLI to control krd
 */

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/agrinman/kr"
	"github.com/agrinman/kr/krdclient"
	"github.com/atotto/clipboard"
	"github.com/fatih/color"
	"github.com/urfave/cli"
	"golang.org/x/crypto/ssh"
)

var sshConfigString = "# Added by Kryptonite\\nHost \\*\\n\\tPKCS11Provider \\/usr\\/local\\/lib\\/kr-pkcs11.so"
var cleanSSHConfigString = fmt.Sprintf("s/\\s*%s//g", sshConfigString)
var cleanSSHConfigCommand = []string{"perl", "-0777", "-pi", "-e", cleanSSHConfigString, os.Getenv("HOME") + "/.ssh/config"}

func PrintFatal(msg string, args ...interface{}) {
	if len(args) == 0 {
		PrintErr(msg)
	} else {
		PrintErr(msg, args...)
	}
	os.Exit(1)
}

func PrintErr(msg string, args ...interface{}) {
	os.Stderr.WriteString(fmt.Sprintf(msg, args...) + "\n")
}

func confirmOrFatal(message string) {
	PrintErr(message + " [y/N] ")
	var c string
	fmt.Scan(&c)
	if len(c) == 0 || c[0] != 'y' {
		PrintFatal("Aborting.")
	}
}

func pairCommand(c *cli.Context) (err error) {
	_, err = krdclient.RequestMe()
	if err == nil {
		confirmOrFatal("Already paired, unpair current session?")
	}
	putConn, err := kr.DaemonDialWithTimeout()
	if err != nil {
		PrintFatal(err.Error())
	}
	defer putConn.Close()

	putPair, err := http.NewRequest("PUT", "/pair", nil)
	if err != nil {
		PrintFatal(err.Error())
	}

	err = putPair.Write(putConn)
	if err != nil {
		PrintFatal(err.Error())
	}

	putReader := bufio.NewReader(putConn)
	putPairResponse, err := http.ReadResponse(putReader, putPair)
	if err != nil {
		PrintFatal(err.Error())
	}
	responseBytes, err := ioutil.ReadAll(putPairResponse.Body)
	if err != nil {
		PrintFatal(err.Error())
	}
	if putPairResponse.StatusCode != http.StatusOK {
		PrintFatal("Pairing failed, ensure your phone and workstation are connected to the internet and try again.")
	}

	qr, err := QREncode(responseBytes)
	if err != nil {
		PrintFatal(err.Error())
	}

	fmt.Println()
	fmt.Println(qr.Terminal)
	fmt.Println("Scan this QR Code with the Kryptonite mobile app to connect it with this workstation. Maximize the window and/or lower your font size if the QR code does not fit.")
	fmt.Println()

	getConn, err := kr.DaemonDialWithTimeout()
	if err != nil {
		PrintFatal(err.Error())
	}
	defer getConn.Close()

	getPair, err := http.NewRequest("GET", "/pair", nil)
	if err != nil {
		PrintFatal(err.Error())
	}
	err = getPair.Write(getConn)
	if err != nil {
		PrintFatal(err.Error())
	}

	//	Check/wait for pairing
	getReader := bufio.NewReader(getConn)
	getResponse, err := http.ReadResponse(getReader, getPair)

	clearCommand := exec.Command("clear")
	clearCommand.Stdout = os.Stdout
	clearCommand.Run()

	if err != nil {
		PrintFatal(err.Error())
	}
	switch getResponse.StatusCode {
	case http.StatusNotFound, http.StatusInternalServerError:
		PrintFatal("Pairing failed, ensure your phone and workstation are connected to the internet and try again.")
	case http.StatusOK:
	default:
		PrintFatal("Pairing failed with error %d", getResponse.StatusCode)
	}
	defer getResponse.Body.Close()
	var me kr.Profile
	responseBody, err := ioutil.ReadAll(getResponse.Body)
	if err != nil {
		PrintFatal(err.Error())
	}
	err = json.Unmarshal(responseBody, &me)

	fmt.Println("Paired successfully with identity")
	authorizedKey := me.AuthorizedKeyString()
	fmt.Println(authorizedKey)
	return
}

func unpairCommand(c *cli.Context) (err error) {
	conn, err := kr.DaemonDialWithTimeout()
	if err != nil {
		PrintFatal(err.Error())
	}
	defer conn.Close()

	deletePair, err := http.NewRequest("DELETE", "/pair", nil)
	if err != nil {
		PrintFatal(err.Error())
	}

	err = deletePair.Write(conn)
	if err != nil {
		PrintFatal(err.Error())
	}

	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, deletePair)
	if err != nil {
		PrintFatal(err.Error())
	}
	switch response.StatusCode {
	case http.StatusNotFound, http.StatusInternalServerError:
		PrintFatal("Unpair failed, ensure the Kryptonite daemon is running with \"kr reset\".")
	case http.StatusOK:
	default:
		PrintFatal("Unpair failed with error %d", response.StatusCode)
	}
	fmt.Println("Unpaired Kryptonite.")
	return
}

func meCommand(c *cli.Context) (err error) {
	me, err := krdclient.RequestMe()
	if err != nil {
		PrintFatal(err.Error())
	}
	authorizedKey := me.AuthorizedKeyString()
	if err != nil {
		PrintFatal(err.Error())
	}
	fmt.Println(authorizedKey)
	PrintErr("\r\nCopy this key to your clipboard using \"kr copy\" or add it to a service like Github using \"kr github\". Type \"kr\" to see all available commands.")
	return
}

func copyCommand(c *cli.Context) (err error) {
	copyKey()
	PrintErr("Public key copied to clipboard.")
	return
}

func copyKey() (err error) {
	me, err := krdclient.RequestMe()
	if err != nil {
		PrintFatal(err.Error())
	}
	authorizedKey := me.AuthorizedKeyString()
	err = clipboard.WriteAll(authorizedKey)
	if err != nil {
		PrintFatal(err.Error())
	}
	return
}

func addCommand(c *cli.Context) (err error) {
	if len(c.Args()) < 1 {
		PrintFatal("kr add <user@server or SSH alias>")
		return
	}
	server := c.Args()[0]

	me, err := krdclient.RequestMe()
	if err != nil {
		PrintFatal("error retrieving your public key: ", err.Error())
	}

	authorizedKey := append([]byte(me.AuthorizedKeyString()), []byte("\n")...)

	PrintErr("Adding your SSH public key to %s", server)

	authorizedKeyReader := bytes.NewReader(authorizedKey)
	sshCommand := exec.Command("ssh", server, "read keys; mkdir -p ~/.ssh && echo $keys >> ~/.ssh/authorized_keys")
	sshCommand.Stdin = authorizedKeyReader
	output, err := sshCommand.CombinedOutput()
	if err != nil {
		PrintFatal(strings.TrimSpace(string(output)))
	}
	return
}

func githubCommand(c *cli.Context) (err error) {
	copyKey()
	PrintErr("Public key copied to clipboard.")
	<-time.After(500 * time.Millisecond)
	PrintErr("Press ENTER to open your web browser to GitHub. Then click \"New SSH Key\" and paste your public key.")
	os.Stdin.Read([]byte{0})
	openBrowser("https://github.com/settings/keys")
	return
}

func bitbucketCommand(c *cli.Context) (err error) {
	copyKey()
	PrintErr("Public key copied to clipboard.")
	<-time.After(500 * time.Millisecond)
	PrintErr("Press ENTER to open your web browser to BitBucket. Then click \"Add key\" and paste your public key.")
	os.Stdin.Read([]byte{0})
	openBrowser("https://bitbucket.org/account/ssh-keys/")
	return
}

func digitaloceanCommand(c *cli.Context) (err error) {
	copyKey()
	PrintErr("Public key copied to clipboard.")
	<-time.After(500 * time.Millisecond)
	PrintErr("Press ENTER to open your web browser to Digital Ocean. Then click \"Add SSH Key\" and paste your public key.")
	os.Stdin.Read([]byte{0})
	openBrowser("https://cloud.digitalocean.com/settings/security")
	return
}

func herokuCommand(c *cli.Context) (err error) {
	_, err = krdclient.RequestMe()
	if err != nil {
		PrintFatal("Failed to retrieve your public key:", err)
	}
	PrintErr("Adding your SSH public key using heroku toolbelt.")
	addKeyCmd := exec.Command("heroku", "keys:add", filepath.Join(os.Getenv("HOME"), ".ssh", "id_kryptonite.pub"))
	addKeyCmd.Stdin = os.Stdin
	addKeyCmd.Stdout = os.Stdout
	addKeyCmd.Stderr = os.Stderr
	addKeyCmd.Run()
	return
}

func gcloudCommand(c *cli.Context) (err error) {
	copyKey()
	PrintErr("Public key copied to clipboard.")
	<-time.After(500 * time.Millisecond)
	PrintErr("Press ENTER to open your web browser to Google Cloud. Then click \"Edit\" and paste your public key.")
	os.Stdin.Read([]byte{0})
	openBrowser("https://console.cloud.google.com/compute/metadata/sshKeys")
	return
}

func awsCommand(c *cli.Context) (err error) {
	copyKey()
	PrintErr("Public key copied to clipboard.")
	<-time.After(500 * time.Millisecond)
	PrintErr("Press ENTER to open your web browser to Amazon Web Services. Then click \"Import Key Pair\" and paste your public key.")
	os.Stdin.Read([]byte{0})
	openBrowser("https://console.aws.amazon.com/ec2/v2/home?#KeyPairs:sort=keyName")
	return
}

func main() {
	app := cli.NewApp()
	app.Name = "kr"
	app.Usage = "communicate with Kryptonite and krd - the Kryptonite daemon"
	app.Version = kr.CURRENT_VERSION.String()
	app.Flags = []cli.Flag{}
	app.Commands = []cli.Command{
		cli.Command{
			Name:   "pair",
			Usage:  "Initiate pairing of this workstation with a phone running Kryptonite.",
			Action: pairCommand,
		},
		cli.Command{
			Name:   "me",
			Usage:  "Print your SSH public key.",
			Action: meCommand,
		},
		cli.Command{
			Name:   "copy",
			Usage:  "Copy your SSH public key to the clipboard.",
			Action: copyCommand,
		},
		cli.Command{
			Name:   "github",
			Usage:  "Upload your public key to GitHub. Copies your public key to the clipboard and opens GitHub settings.",
			Action: githubCommand,
		},
		cli.Command{
			Name:   "bitbucket",
			Usage:  "Upload your public key to BitBucket. Copies your public key to the clipboard and opens BitBucket settings.",
			Action: bitbucketCommand,
		},
		cli.Command{
			Name:   "digital-ocean",
			Usage:  "Upload your public key to Digital Ocean. Copies your public key to the clipboard and opens Digital Ocean settings.",
			Action: digitaloceanCommand,
		},
		cli.Command{
			Name:   "heroku",
			Usage:  "Upload your public key to Heroku. Copies your public key to the clipboard and opens Heroku settings.",
			Action: herokuCommand,
		},
		cli.Command{
			Name:   "aws",
			Usage:  "Upload your public key to Amazon Web Services. Copies your public key to the clipboard and opens the AWS Console.",
			Action: awsCommand,
		},
		cli.Command{
			Name:   "gcloud",
			Usage:  "Upload your public key to Google Cloud. Copies your public key to the clipboard and opens the Google Cloud Console.",
			Action: gcloudCommand,
		},
		cli.Command{
			Name:   "add",
			Usage:  "kr add <user@server or SSH alias> -- add your Kryptonite SSH public key to the server.",
			Action: addCommand,
		},
		cli.Command{
			Name:   "restart",
			Usage:  "Restart the Kryptonite daemon.",
			Action: restartCommand,
		},
		cli.Command{
			Name:   "upgrade",
			Usage:  "Upgrade Kryptonite on this workstation.",
			Action: upgradeCommand,
		},
		cli.Command{
			Name:   "unpair",
			Usage:  "Unpair this workstation from a phone running Kryptonite.",
			Action: unpairCommand,
		},
		cli.Command{
			Name:   "uninstall",
			Usage:  "Uninstall Kryptonite from this workstation.",
			Action: uninstallCommand,
		},
	}
	app.Run(os.Args)
}
