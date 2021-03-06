package repo_manager

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"go.skia.org/infra/go/depot_tools"
	"go.skia.org/infra/go/exec"
	"go.skia.org/infra/go/git"
	"go.skia.org/infra/go/github"
	"go.skia.org/infra/go/sklog"
	"go.skia.org/infra/go/util"
)

const (
	GITHUB_UPSTREAM_REMOTE_NAME = "remote"
)

var (
	// Use this function to instantiate a NewGithubRepoManager. This is able to be
	// overridden for testing.
	NewGithubRepoManager func(context.Context, *GithubRepoManagerConfig, string, *github.GitHub, string, string) (RepoManager, error) = newGithubRepoManager
)

// GithubRepoManagerConfig provides configuration for the Github RepoManager.
type GithubRepoManagerConfig struct {
	DepotToolsRepoManagerConfig
	// Optional config to use if parent path is different than
	// workdir + parent repo.
	GithubParentPath string `json:"githubParentPath"`
}

// Validate the config.
func (c *GithubRepoManagerConfig) Validate() error {
	return c.DepotToolsRepoManagerConfig.Validate()
}

// githubRepoManager is a struct used by the autoroller for managing checkouts.
type githubRepoManager struct {
	*depsRepoManager
	githubClient *github.GitHub
}

// newGithubRepoManager returns a RepoManager instance which operates in the given
// working directory and updates at the given frequency.
func newGithubRepoManager(ctx context.Context, c *GithubRepoManagerConfig, workdir string, githubClient *github.GitHub, recipeCfgFile, serverURL string) (RepoManager, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	wd := path.Join(workdir, strings.TrimSuffix(path.Base(c.DepotToolsRepoManagerConfig.ParentRepo), ".git"))
	drm, err := newDepotToolsRepoManager(ctx, c.DepotToolsRepoManagerConfig, wd, recipeCfgFile, serverURL, nil)
	if err != nil {
		return nil, err
	}
	dr := &depsRepoManager{
		depotToolsRepoManager: drm,
	}
	if c.GithubParentPath != "" {
		dr.parentDir = path.Join(wd, c.GithubParentPath)
	}
	user, err := githubClient.GetAuthenticatedUser()
	if err != nil {
		return nil, err
	}
	dr.user = *user.Login
	gr := &githubRepoManager{
		depsRepoManager: dr,
		githubClient:    githubClient,
	}

	return gr, nil
}

// See documentation for RepoManager interface.
func (rm *githubRepoManager) Update(ctx context.Context) error {
	// Sync the projects.
	rm.repoMtx.Lock()
	defer rm.repoMtx.Unlock()

	sklog.Info("Updating github repository")

	// If parentDir does not exist yet then create the directory structure and
	// populate it.
	if _, err := os.Stat(rm.parentDir); err != nil {
		if os.IsNotExist(err) {
			if err := rm.createAndSyncParent(ctx); err != nil {
				return fmt.Errorf("Could not create and sync %s: %s", rm.parentDir, err)
			}
			// Run gclient hooks to bring in any required binaries.
			if _, err := exec.RunCwd(ctx, rm.parentDir, filepath.Join(rm.depotTools, "gclient"), "runhooks"); err != nil {
				return fmt.Errorf("Error when running gclient runhooks on %s: %s", rm.parentDir, err)
			}
		} else {
			return fmt.Errorf("Error when running os.Stat on %s: %s", rm.parentDir, err)
		}
	}

	// Check to see whether there is an upstream yet.
	remoteOutput, err := git.GitDir(rm.parentDir).Git(ctx, "remote", "show")
	if err != nil {
		return err
	}
	remoteFound := false
	remoteLines := strings.Split(remoteOutput, "\n")
	for _, remoteLine := range remoteLines {
		if remoteLine == GITHUB_UPSTREAM_REMOTE_NAME {
			remoteFound = true
			break
		}
	}
	if !remoteFound {
		if _, err := git.GitDir(rm.parentDir).Git(ctx, "remote", "add", GITHUB_UPSTREAM_REMOTE_NAME, rm.parentRepo); err != nil {
			return err
		}
	}
	// Fetch upstream.
	if _, err := git.GitDir(rm.parentDir).Git(ctx, "fetch", GITHUB_UPSTREAM_REMOTE_NAME, rm.parentBranch); err != nil {
		return err
	}
	// gclient sync to get latest version of child repo to find the next roll
	// rev from.
	if err := rm.createAndSyncParentWithRemote(ctx, GITHUB_UPSTREAM_REMOTE_NAME); err != nil {
		return fmt.Errorf("Could not create and sync parent repo: %s", err)
	}

	// Get the last roll revision.
	lastRollRev, err := rm.getLastRollRev(ctx)
	if err != nil {
		return err
	}

	// Find the number of not-rolled child repo commits.
	notRolled, err := rm.getCommitsNotRolled(ctx, lastRollRev)
	if err != nil {
		return err
	}

	// Get the next roll revision.
	nextRollRev, err := rm.getNextRollRev(ctx, notRolled, lastRollRev)
	if err != nil {
		return err
	}

	rm.infoMtx.Lock()
	defer rm.infoMtx.Unlock()
	rm.lastRollRev = lastRollRev
	rm.nextRollRev = nextRollRev
	rm.commitsNotRolled = len(notRolled)

	sklog.Infof("lastRollRev is: %s", rm.lastRollRev)
	sklog.Infof("nextRollRev is: %s", nextRollRev)
	sklog.Infof("commitsNotRolled: %d", rm.commitsNotRolled)
	return nil
}

// See documentation for RepoManager interface.
func (rm *githubRepoManager) CreateNewRoll(ctx context.Context, from, to string, emails []string, cqExtraTrybots string, dryRun bool) (int64, error) {
	rm.repoMtx.Lock()
	defer rm.repoMtx.Unlock()

	sklog.Info("Creating a new Github Roll")

	// Clean the checkout, get onto a fresh branch.
	if err := rm.cleanParentWithRemote(ctx, GITHUB_UPSTREAM_REMOTE_NAME); err != nil {
		return 0, err
	}
	if _, err := git.GitDir(rm.parentDir).Git(ctx, "checkout", fmt.Sprintf("%s/%s", GITHUB_UPSTREAM_REMOTE_NAME, rm.parentBranch), "-b", ROLL_BRANCH); err != nil {
		return 0, err
	}
	// Defer cleanup.
	defer func() {
		util.LogErr(rm.cleanParentWithRemote(ctx, GITHUB_UPSTREAM_REMOTE_NAME))
	}()

	// Make sure the forked repo is at the same hash as the target repo before
	// creating the pull request.
	if _, err := git.GitDir(rm.parentDir).Git(ctx, "push", "origin", ROLL_BRANCH, "-f"); err != nil {
		return 0, err
	}

	// Make sure the right name and email are set.
	if _, err := git.GitDir(rm.parentDir).Git(ctx, "config", "user.name", rm.user); err != nil {
		return 0, err
	}
	if _, err := git.GitDir(rm.parentDir).Git(ctx, "config", "user.email", rm.user); err != nil {
		return 0, err
	}

	// Run "gclient setdep".
	args := []string{"setdep", "-r", fmt.Sprintf("%s@%s", rm.childPath, to)}
	if _, err := exec.RunCommand(ctx, &exec.Command{
		Dir:  rm.parentDir,
		Env:  depot_tools.Env(rm.depotTools),
		Name: rm.gclient,
		Args: args,
	}); err != nil {
		return 0, err
	}

	// Make third_party/ match the new DEPS.
	if _, err := exec.RunCommand(ctx, &exec.Command{
		Dir:  rm.depsRepoManager.parentDir,
		Env:  depot_tools.Env(rm.depotTools),
		Name: rm.gclient,
		Args: []string{"sync"},
	}); err != nil {
		return 0, fmt.Errorf("Error when running gclient sync to make third_party/ match the new DEPS: %s", err)
	}

	// Run the pre-upload steps.
	for _, s := range rm.PreUploadSteps() {
		if err := s(ctx, rm.parentDir); err != nil {
			return 0, fmt.Errorf("Error when running pre-upload step: %s", err)
		}
	}

	// Build the commit message.
	commitMsg, err := rm.buildCommitMsg(ctx, from, to, cqExtraTrybots, nil)
	if err != nil {
		return 0, err
	}

	// Commit.
	if _, err := git.GitDir(rm.parentDir).Git(ctx, "commit", "-a", "-m", commitMsg); err != nil {
		return 0, err
	}

	// Push to the forked repository.
	if _, err := git.GitDir(rm.parentDir).Git(ctx, "push", "origin", ROLL_BRANCH, "-f"); err != nil {
		return 0, err
	}

	// Grab the first line of the commit msg to use as the title of the pull request.
	title := strings.Split(commitMsg, "\n")[0]
	// Shorten the child path in the title for brevity.
	childPathTokens := strings.Split(rm.childPath, "/")
	shortenedChildName := childPathTokens[len(childPathTokens)-1]
	title = strings.Replace(title, rm.childPath+"/", shortenedChildName, 1)
	// Use the remaining part of the commit message as the pull request description.
	descComment := strings.Split(commitMsg, "\n")[1:]
	// Create a pull request.
	headBranch := fmt.Sprintf("%s:%s", strings.Split(rm.user, "@")[0], ROLL_BRANCH)
	pr, err := rm.githubClient.CreatePullRequest(title, rm.parentBranch, headBranch, strings.Join(descComment, "\n"))
	if err != nil {
		return 0, err
	}

	// Mention the sheriffs on the pull request so that they are automatically
	// subscribed to it.
	mentions := []string{}
	for _, e := range emails {
		m := fmt.Sprintf("@%s", strings.Split(e, "@")[0])
		mentions = append(mentions, m)
	}
	if err := rm.githubClient.AddComment(pr.GetNumber(), fmt.Sprintf("%s : New roll has been created by %s", strings.Join(mentions, " "), rm.serverURL)); err != nil {
		return 0, err
	}

	return int64(pr.GetNumber()), nil
}

// See documentation for RepoManager interface.
func (rm *githubRepoManager) User() string {
	return rm.user
}

// See documentation for RepoManager interface.
func (rm *githubRepoManager) GetFullHistoryUrl() string {
	user := strings.Split(rm.user, "@")[0]
	return fmt.Sprintf("https://github.com/%s/%s/pulls/%s", rm.githubClient.RepoOwner, rm.githubClient.RepoName, user)
}

// See documentation for RepoManager interface.
func (rm *githubRepoManager) GetIssueUrlBase() string {
	return fmt.Sprintf("https://github.com/%s/%s/pull/", rm.githubClient.RepoOwner, rm.githubClient.RepoName)
}
