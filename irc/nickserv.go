// Copyright (c) 2017 Daniel Oaks <daniel@danieloaks.net>
// released under the MIT license

package irc

import (
	"fmt"
	"strings"
)

// TODO: "email" is an oversimplification here; it's actually any callback, e.g.,
// person@example.com, mailto:person@example.com, tel:16505551234.
const nickservHelp = `NickServ lets you register and log into a user account.

To register an account:
	/NS REGISTER username email [password]
Leave out [password] if you're registering using your client certificate fingerprint.
The server may or may not allow you to register anonymously (by sending * as your
email address).

To verify an account (if you were sent a verification code):
	/NS VERIFY username code

To unregister an account:
	/NS UNREGISTER [username]
Leave out [username] if you're unregistering the user you're currently logged in as.

To login to an account:
	/NS IDENTIFY [username password]
Leave out [username password] to use your client certificate fingerprint. Otherwise,
the given username and password will be used.

To see account information:
	/NS INFO [username]
Leave out [username] to see your own account information.

To associate your current nick with the account you're logged into:
	/NS GROUP

To disassociate a nick with the account you're logged into:
	/NS DROP [nickname]
Leave out [nickname] to drop your association with your current nickname.`

// extractParam extracts a parameter from the given string, returning the param and the rest of the string.
func extractParam(line string) (string, string) {
	rawParams := strings.SplitN(strings.TrimSpace(line), " ", 2)
	param0 := rawParams[0]
	var param1 string
	if 1 < len(rawParams) {
		param1 = strings.TrimSpace(rawParams[1])
	}
	return param0, param1
}

// nickservNoticeHandler handles NOTICEs that NickServ receives.
func (server *Server) nickservNoticeHandler(client *Client, message string, rb *ResponseBuffer) {
	// do nothing
}

// send a notice from the NickServ "nick"
func nsNotice(rb *ResponseBuffer, text string) {
	rb.Add(nil, "NickServ", "NOTICE", rb.target.Nick(), text)
}

// nickservPrivmsgHandler handles PRIVMSGs that NickServ receives.
func (server *Server) nickservPrivmsgHandler(client *Client, message string, rb *ResponseBuffer) {
	command, params := extractParam(message)
	command = strings.ToLower(command)

	if command == "help" {
		for _, line := range strings.Split(nickservHelp, "\n") {
			nsNotice(rb, line)
		}
	} else if command == "register" {
		// get params
		username, afterUsername := extractParam(params)
		email, passphrase := extractParam(afterUsername)
		server.nickservRegisterHandler(client, username, email, passphrase, rb)
	} else if command == "verify" {
		username, code := extractParam(params)
		server.nickservVerifyHandler(client, username, code, rb)
	} else if command == "identify" {
		username, passphrase := extractParam(params)
		server.nickservIdentifyHandler(client, username, passphrase, rb)
	} else if command == "unregister" {
		username, _ := extractParam(params)
		server.nickservUnregisterHandler(client, username, rb)
	} else if command == "ghost" {
		nick, _ := extractParam(params)
		server.nickservGhostHandler(client, nick, rb)
	} else if command == "info" {
		nick, _ := extractParam(params)
		server.nickservInfoHandler(client, nick, rb)
	} else if command == "group" {
		server.nickservGroupHandler(client, rb)
	} else if command == "drop" {
		nick, _ := extractParam(params)
		server.nickservDropHandler(client, nick, false, rb)
	} else if command == "sadrop" {
		nick, _ := extractParam(params)
		server.nickservDropHandler(client, nick, true, rb)
	} else {
		nsNotice(rb, client.t("Command not recognised. To see the available commands, run /NS HELP"))
	}
}

func (server *Server) nickservUnregisterHandler(client *Client, username string, rb *ResponseBuffer) {
	if !server.AccountConfig().Registration.Enabled {
		nsNotice(rb, client.t("Account registration has been disabled"))
		return
	}

	if username == "" {
		username = client.Account()
	}
	if username == "" {
		nsNotice(rb, client.t("You're not logged into an account"))
		return
	}
	cfname, err := CasefoldName(username)
	if err != nil {
		nsNotice(rb, client.t("Invalid username"))
		return
	}
	if !(cfname == client.Account() || client.HasRoleCapabs("unregister")) {
		nsNotice(rb, client.t("Insufficient oper privs"))
		return
	}

	if cfname == client.Account() {
		client.server.accounts.Logout(client)
	}

	err = server.accounts.Unregister(cfname)
	if err == errAccountDoesNotExist {
		nsNotice(rb, client.t(err.Error()))
	} else if err != nil {
		nsNotice(rb, client.t("Error while unregistering account"))
	} else {
		nsNotice(rb, fmt.Sprintf(client.t("Successfully unregistered account %s"), cfname))
	}
}

func (server *Server) nickservVerifyHandler(client *Client, username string, code string, rb *ResponseBuffer) {
	err := server.accounts.Verify(client, username, code)

	var errorMessage string
	if err == errAccountVerificationInvalidCode || err == errAccountAlreadyVerified {
		errorMessage = err.Error()
	} else if err != nil {
		errorMessage = errAccountVerificationFailed.Error()
	}

	if errorMessage != "" {
		nsNotice(rb, client.t(errorMessage))
		return
	}

	sendSuccessfulRegResponse(client, rb, true)
}

func (server *Server) nickservRegisterHandler(client *Client, username, email, passphrase string, rb *ResponseBuffer) {
	if !server.AccountConfig().Registration.Enabled {
		nsNotice(rb, client.t("Account registration has been disabled"))
		return
	}

	if username == "" {
		nsNotice(rb, client.t("No username supplied"))
		return
	}

	certfp := client.certfp
	if passphrase == "" && certfp == "" {
		nsNotice(rb, client.t("You need to either supply a passphrase or be connected via TLS with a client cert"))
		return
	}

	if client.LoggedIntoAccount() {
		if server.AccountConfig().Registration.AllowMultiplePerConnection {
			server.accounts.Logout(client)
		} else {
			nsNotice(rb, client.t("You're already logged into an account"))
			return
		}
	}

	config := server.AccountConfig()
	var callbackNamespace, callbackValue string
	noneCallbackAllowed := false
	for _, callback := range config.Registration.EnabledCallbacks {
		if callback == "*" {
			noneCallbackAllowed = true
		}
	}
	// XXX if ACC REGISTER allows registration with the `none` callback, then ignore
	// any callback that was passed here (to avoid confusion in the case where the ircd
	// has no mail server configured). otherwise, register using the provided callback:
	if noneCallbackAllowed {
		callbackNamespace = "*"
	} else {
		callbackNamespace, callbackValue = parseCallback(email, config)
		if callbackNamespace == "" {
			nsNotice(rb, client.t("Registration requires a valid e-mail address"))
			return
		}
	}

	// get and sanitise account name
	account := strings.TrimSpace(username)

	err := server.accounts.Register(client, account, callbackNamespace, callbackValue, passphrase, client.certfp)
	if err == nil {
		if callbackNamespace == "*" {
			err = server.accounts.Verify(client, account, "")
			if err == nil {
				sendSuccessfulRegResponse(client, rb, true)
			}
		} else {
			messageTemplate := client.t("Account created, pending verification; verification code has been sent to %s:%s")
			message := fmt.Sprintf(messageTemplate, callbackNamespace, callbackValue)
			nsNotice(rb, message)
		}
	}

	// details could not be stored and relevant numerics have been dispatched, abort
	if err != nil {
		errMsg := "Could not register"
		if err == errCertfpAlreadyExists {
			errMsg = "An account already exists for your certificate fingerprint"
		} else if err == errAccountAlreadyRegistered {
			errMsg = "Account already exists"
		}
		nsNotice(rb, client.t(errMsg))
		return
	}
}

func (server *Server) nickservIdentifyHandler(client *Client, username, passphrase string, rb *ResponseBuffer) {
	// fail out if we need to
	if !server.AccountConfig().AuthenticationEnabled {
		nsNotice(rb, client.t("Login has been disabled"))
		return
	}

	loginSuccessful := false

	// try passphrase
	if username != "" && passphrase != "" {
		err := server.accounts.AuthenticateByPassphrase(client, username, passphrase)
		loginSuccessful = (err == nil)
	}

	// try certfp
	if !loginSuccessful && client.certfp != "" {
		err := server.accounts.AuthenticateByCertFP(client)
		loginSuccessful = (err == nil)
	}

	if loginSuccessful {
		sendSuccessfulSaslAuth(client, rb, true)
	} else {
		nsNotice(rb, client.t("Could not login with your TLS certificate or supplied username/password"))
	}
}

func (server *Server) nickservGhostHandler(client *Client, nick string, rb *ResponseBuffer) {
	if !server.AccountConfig().NickReservation.Enabled {
		nsNotice(rb, client.t("Nickname reservation is disabled"))
		return
	}

	account := client.Account()
	if account == "" || server.accounts.NickToAccount(nick) != account {
		nsNotice(rb, client.t("You don't own that nick"))
		return
	}

	ghost := server.clients.Get(nick)
	if ghost == nil {
		nsNotice(rb, client.t("No such nick"))
		return
	} else if ghost == client {
		nsNotice(rb, client.t("You can't GHOST yourself (try /QUIT instead)"))
		return
	}

	ghost.Quit(fmt.Sprintf(ghost.t("GHOSTed by %s"), client.Nick()))
	ghost.destroy(false)
}

func (server *Server) nickservGroupHandler(client *Client, rb *ResponseBuffer) {
	if !server.AccountConfig().NickReservation.Enabled {
		nsNotice(rb, client.t("Nickname reservation is disabled"))
		return
	}

	account := client.Account()
	if account == "" {
		nsNotice(rb, client.t("You're not logged into an account"))
		return
	}

	nick := client.NickCasefolded()
	err := server.accounts.SetNickReserved(client, nick, false, true)
	if err == nil {
		nsNotice(rb, fmt.Sprintf(client.t("Successfully grouped nick %s with your account"), nick))
	} else if err == errAccountTooManyNicks {
		nsNotice(rb, client.t("You have too many nicks reserved already (you can remove some with /NS DROP)"))
	} else if err == errNicknameReserved {
		nsNotice(rb, client.t("That nickname is already reserved by someone else"))
	} else {
		nsNotice(rb, client.t("Error reserving nickname"))
	}
}

func (server *Server) nickservInfoHandler(client *Client, nick string, rb *ResponseBuffer) {
	if nick == "" {
		nick = client.Nick()
	}

	accountName := nick
	if server.AccountConfig().NickReservation.Enabled {
		accountName = server.accounts.NickToAccount(nick)
		if accountName == "" {
			nsNotice(rb, client.t("That nickname is not registered"))
			return
		}
	}

	account, err := server.accounts.LoadAccount(accountName)
	if err != nil || !account.Verified {
		nsNotice(rb, client.t("Account does not exist"))
	}

	nsNotice(rb, fmt.Sprintf(client.t("Account: %s"), account.Name))
	registeredAt := account.RegisteredAt.Format("Jan 02, 2006 15:04:05Z")
	nsNotice(rb, fmt.Sprintf(client.t("Registered at: %s"), registeredAt))
	// TODO nicer formatting for this
	for _, nick := range account.AdditionalNicks {
		nsNotice(rb, fmt.Sprintf(client.t("Additional grouped nick: %s"), nick))
	}
}

func (server *Server) nickservDropHandler(client *Client, nick string, sadrop bool, rb *ResponseBuffer) {
	if sadrop {
		if !client.HasRoleCapabs("unregister") {
			nsNotice(rb, client.t("Insufficient oper privs"))
			return
		}
	}

	err := server.accounts.SetNickReserved(client, nick, sadrop, false)
	if err == nil {
		nsNotice(rb, fmt.Sprintf(client.t("Successfully ungrouped nick %s with your account"), nick))
	} else if err == errAccountNotLoggedIn {
		nsNotice(rb, fmt.Sprintf(client.t("You're not logged into an account")))
	} else if err == errAccountCantDropPrimaryNick {
		nsNotice(rb, fmt.Sprintf(client.t("You can't ungroup your primary nickname (try unregistering your account instead)")))
	} else if err == errNicknameReserved {
		nsNotice(rb, fmt.Sprintf(client.t("That nickname is already reserved by someone else")))
	} else {
		nsNotice(rb, client.t("Error ungrouping nick"))
	}
}
