package main

// Gracefully shuts down groups of bots via the poorly named "terminate" api

import (
	"flag"
	"fmt"
	"path"
	"path/filepath"
	"regexp"

	"go.skia.org/infra/go/auth"
	"go.skia.org/infra/go/common"
	"go.skia.org/infra/go/sklog"
	"go.skia.org/infra/go/swarming"
	"go.skia.org/infra/go/util"
)

var (
	dimensions  = common.NewMultiStringFlag("dimension", nil, "Colon-separated key/value pair, eg: \"os:Android\" Dimensions with which to find matching tasks. Can specify multiple dimensions, bots will need to match all dimensions.")
	dryrun      = flag.Bool("dryrun", false, "Don't actually terminate the bots.")
	excludeBots = common.NewMultiStringFlag("exclude_bot", nil, "Bots which should not be affected if they match the other criteria.  Can specify multiple regexes or strings.")
	includeBots = common.NewMultiStringFlag("include_bot", nil, "Bots which will be affected, even if they don't match the other criteria.  Can specify multiple regexes or strings.")
	pool        = flag.String("pool", "", "Which Swarming pool to use.")
	verbose     = flag.Bool("verbose", false, "Display a lot of information.")
	workdir     = flag.String("workdir", ".", "Working directory used to find the google_storage_token.data Optional, but recommended not to use CWD.")
)

var (
	includeRegs []*regexp.Regexp
	excludeRegs []*regexp.Regexp
)

func main() {
	// Setup, parse args.
	defer common.LogPanic()
	common.Init()

	if *pool == "" {
		sklog.Fatal("--pool is required.")
	}

	if *dimensions == nil && *includeBots == nil {
		sklog.Fatal("So one does not accidentally shutdown the entire pool, you must specify a dimension or an include rule.")
	}
	requestedDims, err := swarming.ParseDimensionFlags(dimensions)
	if err != nil {
		sklog.Fatalf("Problem parsing dimensions: %s", err)
	}
	sklog.Infof("Using dimensions: %q", requestedDims)

	includeRegs, err = parseRegex(*includeBots)
	if err != nil {
		sklog.Fatalf("Invalid regexp detected in include_bot %q: %s", *includeBots, err)
	}
	excludeRegs, err = parseRegex(*excludeBots)
	if err != nil {
		sklog.Fatalf("Invalid regexp detected in exclude_bot %q: %s", *excludeBots, err)
	}

	*workdir, err = filepath.Abs(*workdir)
	if err != nil {
		sklog.Fatal(err)
	}

	// Authenticated HTTP client.
	oauthCacheFile := path.Join(*workdir, "google_storage_token.data")
	httpClient, err := auth.NewClient(true, oauthCacheFile, swarming.AUTH_SCOPE)
	if err != nil {
		sklog.Fatalf("Could not authenticate. Did you get the swarming client_secret and put it in %s? : %s", *workdir, err)
	}

	// Swarming API client.
	swarmApi, err := swarming.NewApiClient(httpClient, swarming.SWARMING_SERVER)
	if err != nil {
		sklog.Fatal(err)
	}

	// Obtain the list of bots in this pool.
	bots, err := swarmApi.ListBots(map[string]string{
		"pool": *pool,
	})
	if err != nil {
		sklog.Fatal(err)
	}
	logIfVerbose("%d bots in the pool", len(bots))

	matched := []string{}

	// We manually go through the list because the bot list API does not
	// support include/exclude
	for _, bot := range bots {
		if matchesAny(bot.BotId, excludeRegs) {
			logIfVerbose("Excluding %s based on exclude_bot", bot.BotId)
			continue
		}
		if matchesAny(bot.BotId, includeRegs) {
			logIfVerbose("Including %s based on include_bot", bot.BotId)
			matched = append(matched, bot.BotId)
			continue
		}
		if len(requestedDims) == 0 {
			// No specified dimensions, so only go off the include/exclude list
			continue
		}
		// Check that the bot matches all the specified dimensions.
		matchesAll := true
		for k, d := range requestedDims {
			has := false
			for _, dim := range bot.Dimensions {
				if k == dim.Key {
					has = util.In(d, dim.Value)
				}
			}
			if !has {
				matchesAll = false
				break
			}
		}
		if matchesAll {
			logIfVerbose("Including %s based on dimension(s)", bot.BotId)
			matched = append(matched, bot.BotId)
		}
	}

	if len(matched) == 0 {
		sklog.Infof("No matches.  Quitting.")
		return
	}
	sklog.Infof("The following bots will be scheduled for a graceful shutdown %q", matched)
	if *dryrun {
		sklog.Error("Exiting because of dryrun mode")
		return
	}
	sklog.Error("Continue?")
	if conf, err := askForConfirmation(); err != nil || !conf {
		sklog.Errorf("Not continuing (Error: %v)", err)
		return
	}

	for _, m := range matched {
		if r, err := swarmApi.GracefullyShutdownBot(m); err != nil {
			sklog.Errorf("Problem shutting down %s: %s", m, err)
		} else {
			logIfVerbose("Response from shutting down %s: %#v", m, r)
			sklog.Infof("Task for shutting down %s: https://chromium-swarm.appspot.com/task?id=%s", m, r.TaskId)
		}
	}

}

func askForConfirmation() (bool, error) {
	var response string
	if _, err := fmt.Scanln(&response); err != nil {
		return false, err
	}
	if response == "y" {
		return true, nil
	} else if response == "n" {
		return false, nil
	} else {
		sklog.Info("Please type 'y' or 'n' and then press enter:")
		return askForConfirmation()
	}
}

func logIfVerbose(f string, args ...interface{}) {
	if *verbose {
		sklog.Infof(f, args...)
	}
}

func parseRegex(flags common.MultiString) (retval []*regexp.Regexp, e error) {
	if len(flags) == 0 {
		return retval, nil
	}

	for _, s := range flags {
		r, err := regexp.Compile(s)
		if err != nil {
			return nil, err
		}
		retval = append(retval, r)
	}
	return retval, nil
}

func matchesAny(s string, xr []*regexp.Regexp) bool {
	for _, r := range xr {
		if r.MatchString(s) {
			return true
		}
	}
	return false
}
