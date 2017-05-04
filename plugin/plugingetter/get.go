package plugingetter

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"runtime"
	"strings"

	"golang.org/x/net/html"

	getter "github.com/hashicorp/go-getter"
	"github.com/hashicorp/terraform/plugin/discovery"
)

const releasesURL = "https://releases.hashicorp.com/"

// pluginURL generates URLs to lookup the versions of a plugin, or the file path.
//
// The URL for releases follows the pattern:
//    https://releases.hashicorp.com/terraform-providers/terraform-provider-name/ +
//        terraform-provider-name_<x.y.z>/terraform-provider-name_<x.y.z>_<os>_<arch>.<ext>
type pluginURL struct {
	// The name prefix common to all plugins of this type.
	// This is either `terraform-provider` or `terraform-provisioner`.
	pluginType string
}

// base returns the top level directory for all plugins of this type
func (p pluginURL) base() string {
	// the top level directory is the plural form of the plugin type
	return releasesURL + p.pluginType + "s"
}

// versions returns the url to the directory to list available versions for this plugin
func (p pluginURL) versions(name string) string {
	return fmt.Sprintf("%s/%s-%s", p.base(), p.pluginType, name)
}

// file returns the full path to a plugin based on the plugin name,
// version, GOOS and GOARCH.
func (p pluginURL) file(name, version string) string {
	releasesDir := fmt.Sprintf("%s-%s_%s/", p.pluginType, name, version)
	fileName := fmt.Sprintf("%s-%s_%s_%s_%s.zip", p.pluginType, name, version, runtime.GOOS, runtime.GOARCH)
	return fmt.Sprintf("%s/%s/%s", p.versions(name), releasesDir, fileName)
}

var providersURL = pluginURL{"terraform-provider"}
var provisionersURL = pluginURL{"terraform-provisioners"}

// GetProviders fetches the provider plugins listed in requirements, and copies
// them to the dst directory.
//
// TODO: verify checksum and signature
func GetProviders(dst string, requirements discovery.PluginRequirements) error {
	for provider, req := range requirements {
		versions, err := listProviderVersions(provider)
		// TODO: return multiple errors
		if err != nil {
			return err
		}

		version := newestVersion(versions, req)
		if version == nil {
			// TODO: return all errors
			return fmt.Errorf("no version of %q available that fulfills constraints %s", provider, req)
		}

		url := providersURL.file(provider, version.String())

		log.Printf("[DEBUG] getting provider %q version %q at %s", provider, version, url)
		return getter.Get(dst, url)
	}

	return nil
}

// take the list of available versions for a plugin, and the required
// Constraints, and return the latest available version that satisfies the
// constraints.
func newestVersion(available []*discovery.Version, required discovery.Constraints) *discovery.Version {
	var latest *discovery.Version
	for _, v := range available {
		if required.Has(*v) {
			if latest == nil {
				latest = v
				continue
			}

			if v.NewerThan(*latest) {
				latest = v
			}
		}
	}

	return latest
}

// list the version available for the named plugin
func listProviderVersions(name string) ([]*discovery.Version, error) {
	versions, err := listPluginVersions(providersURL.versions(name))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch versions for provider %q: %s", name, err)
	}
	return versions, nil
}

func listProvisionerVersions(name string) ([]*discovery.Version, error) {
	versions, err := listPluginVersions(provisionersURL.versions(name))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch versions for provisioner %q: %s", name, err)
	}

	return versions, nil
}

// return a list of the plugin versions at the given URL
// TODO: This doesn't yet take into account plugin protocol version.
//       That may have to be checked via an http header via a separate request
//       to each plugin file.
func listPluginVersions(url string) ([]*discovery.Version, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		log.Printf("[ERROR] failed to fetch plugin versions from %s\n%s\n%s", url, resp.Status, body)
		return nil, errors.New(resp.Status)
	}

	body, err := html.Parse(resp.Body)
	if err != nil {
		log.Fatal(err)
	}

	names := []string{}

	// all we need to do is list links on the directory listing page that look like plugins
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			c := n.FirstChild
			if c != nil && c.Type == html.TextNode && strings.HasPrefix(c.Data, "terraform-") {
				names = append(names, c.Data)
				fmt.Println(c.Data)
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(body)

	var versions []*discovery.Version

	for _, name := range names {
		parts := strings.SplitN(name, "_", 2)
		if len(parts) == 2 && parts[1] != "" {
			v, err := discovery.VersionStr(parts[1]).Parse()
			if err != nil {
				// filter invalid versions scraped from the page
				log.Printf("[WARN] invalid version found for %q: %s", name, err)
				continue
			}

			versions = append(versions, &v)
		}
	}

	return versions, nil
}
