package main

import (
	"fmt"
	assert "github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"

	"github.com/aerokube/ggr/config"
)

func init() {
	verbose = true
}

func TestEmptyListOfHosts(t *testing.T) {
	host, index := choose(config.Hosts{})
	assert.Nil(t, host)
	assert.Equal(t, index, -1)
}

func TestNothingToChoose(t *testing.T) {
	host, index := choose(config.Hosts{config.Host{Count: 0}, config.Host{Count: 0}})
	assert.Nil(t, host)
	assert.Equal(t, index, -1)
}

func TestChooseFirst(t *testing.T) {
	host, index := choose(config.Hosts{config.Host{Name: "first", Count: 1}, config.Host{Name: "mid", Count: 0}, config.Host{Name: "last", Count: 0}})
	assert.Equal(t, host.Name, "first")
	assert.Equal(t, index, 0)
}

func TestChooseMid(t *testing.T) {
	host, index := choose(config.Hosts{config.Host{Name: "first", Count: 0}, config.Host{Name: "mid", Count: 1}, config.Host{Name: "last", Count: 0}})
	assert.Equal(t, host.Name, "mid")
	assert.Equal(t, index, 1)
}

func TestChooseLast(t *testing.T) {
	host, index := choose(config.Hosts{config.Host{Name: "first", Count: 0}, config.Host{Name: "mid", Count: 0}, config.Host{Name: "last", Count: 1}})
	assert.Equal(t, host.Name, "last")
	assert.Equal(t, index, 2)
}

var (
	browsersWithMultipleVersions = &ggrBrowsers{config.Browsers{Browsers: []config.Browser{
		{Name: "browser", DefaultVersion: "2.0", Versions: []config.Version{
			{Number: "2.0", Regions: []config.Region{
				{Hosts: config.Hosts{
					config.Host{Name: "browser-2.0"},
				}},
			}},
			{Number: "1.0", Regions: []config.Region{
				{Hosts: config.Hosts{
					config.Host{Name: "browser-1.0"},
				}},
			}},
			{Number: "", Regions: []config.Region{
				{Hosts: config.Hosts{
					config.Host{Name: "browser"},
				}},
			}},
		}}}}}

	browsersWithMultipleRegions = &ggrBrowsers{config.Browsers{Browsers: []config.Browser{
		{Name: "browser", DefaultVersion: "1.0", Versions: []config.Version{
			{Number: "1.0", Regions: []config.Region{
				{Name: "e", Hosts: config.Hosts{
					config.Host{Name: "browser-e-1.0", Port: 4444},
				}},
				{Name: "f", Hosts: config.Hosts{
					config.Host{Name: "browser-f-1.0", Port: 4444},
				}},
			}},
		}}}}}

	browsersWithMultiplePlatforms = &ggrBrowsers{config.Browsers{Browsers: []config.Browser{
		{Name: "browser", DefaultVersion: "2.0", DefaultPlatform: "LINUX", Versions: []config.Version{
			{Number: "2.0", Platform: "LINUX", Regions: []config.Region{
				{Hosts: config.Hosts{
					config.Host{Name: "browser-2.0-linux"},
				}},
			}},
			{Number: "2.0", Platform: "WINDOWS", Regions: []config.Region{
				{Hosts: config.Hosts{
					config.Host{Name: "browser-2.0-windows"},
				}},
			}},
		}}}}}
)

func TestFindDefaultVersion(t *testing.T) {
	hosts, version, _ := browsersWithMultipleVersions.find("browser", "", "", newSet(), newSet())
	assert.Equal(t, version, "2.0")
	assert.Len(t, hosts, 1)
	assert.Equal(t, hosts[0].Name, "browser-2.0")
}

func TestFindVersion(t *testing.T) {
	hosts, version, _ := browsersWithMultipleVersions.find("browser", "1.0", "LINUX", newSet(), newSet())
	assert.Equal(t, version, "1.0")
	assert.Len(t, hosts, 1)
	assert.Equal(t, hosts[0].Name, "browser-1.0")
}

func TestFindVersionByPrefix(t *testing.T) {
	hosts, version, _ := browsersWithMultipleVersions.find("browser", "1", "", newSet(), newSet())
	assert.Equal(t, version, "1.0")
	assert.Len(t, hosts, 1)
	assert.Equal(t, hosts[0].Name, "browser-1.0")
}

func TestVersionNotFound(t *testing.T) {
	hosts, version, _ := browsersWithMultipleVersions.find("browser", "missing", "", newSet(), newSet())
	assert.Equal(t, version, "missing")
	assert.Empty(t, hosts)
}

func TestFindWithExcludedRegions(t *testing.T) {
	hosts, version, _ := browsersWithMultipleRegions.find("browser", "1.0", "", newSet(), newSet("f"))
	assert.Equal(t, version, "1.0")
	assert.Len(t, hosts, 1)
	assert.Equal(t, hosts[0].Name, "browser-e-1.0")
}

func TestFindWithExcludedRegionsExhausted(t *testing.T) {
	hosts, _, excludedRegions := browsersWithMultipleRegions.find("browser", "1.0", "", newSet(), newSet("e", "f"))
	assert.Len(t, hosts, 2)
	assert.Equal(t, excludedRegions.size(), 0)
}

func TestFindWithExcludedHosts(t *testing.T) {
	hosts, version, _ := browsersWithMultipleRegions.find("browser", "1.0", "", newSet("browser-e-1.0:4444"), newSet())
	assert.Equal(t, version, "1.0")
	assert.Len(t, hosts, 1)
	assert.Equal(t, hosts[0].Name, "browser-f-1.0")
}

func TestFindWithDefaultPlatform(t *testing.T) {
	hosts, version, _ := browsersWithMultiplePlatforms.find("browser", "2.0", "", newSet(), newSet())
	assert.Equal(t, version, "2.0")
	assert.Len(t, hosts, 1)
	assert.Equal(t, hosts[0].Name, "browser-2.0-linux")
}

func TestFindWithAnyPlatform(t *testing.T) {
	hosts, version, _ := browsersWithMultiplePlatforms.find("browser", "2.0", "ANY", newSet(), newSet())
	assert.Equal(t, version, "2.0")
	assert.Len(t, hosts, 1)
	assert.Equal(t, hosts[0].Name, "browser-2.0-linux")
}

func TestFindWithPlatform(t *testing.T) {
	hosts, version, _ := browsersWithMultiplePlatforms.find("browser", "2.0", "LINUX", newSet(), newSet())
	assert.Equal(t, version, "2.0")
	assert.Len(t, hosts, 1)
	assert.Equal(t, hosts[0].Name, "browser-2.0-linux")
}

func TestFindWithPlatformLowercase(t *testing.T) {
	hosts, version, _ := browsersWithMultiplePlatforms.find("browser", "2.0", "windows", newSet(), newSet())
	assert.Equal(t, version, "2.0")
	assert.Len(t, hosts, 1)
	assert.Equal(t, hosts[0].Name, "browser-2.0-windows")
}

func TestFindWithPlatformPrefix(t *testing.T) {
	hosts, version, _ := browsersWithMultiplePlatforms.find("browser", "2.0", "WIN", newSet(), newSet())
	assert.Equal(t, version, "2.0")
	assert.Len(t, hosts, 1)
	assert.Equal(t, hosts[0].Name, "browser-2.0-windows")
}

func TestReadNotExistingConfig(t *testing.T) {
	tmp, err := os.CreateTemp("", "config")
	if err != nil {
		t.Fatal(err)
	}
	err = os.Remove(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	var browsers config.Browsers
	err = readConfig(tmp.Name(), &browsers)

	assert.Error(t, err)
	assert.Equal(t, err.Error(), fmt.Sprintf("error reading configuration file %s: open %s: no such file or directory", tmp.Name(), tmp.Name()))
}

func TestParseInvalidConfig(t *testing.T) {
	tmp, err := os.CreateTemp("", "config")
	defer os.Remove(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	_, err = tmp.Write([]byte("this is not valid xml"))
	if err != nil {
		t.Fatal(err)
	}
	err = tmp.Close()
	if err != nil {
		t.Fatal(err)
	}
	var browsers config.Browsers
	err = readConfig(tmp.Name(), &browsers)

	assert.Error(t, err)
	assert.Equal(t, err.Error(), fmt.Sprintf("error parsing configuration file %s: EOF", tmp.Name()))
}

func TestParseConfig(t *testing.T) {
	testParseConfig(t, `<qa:browsers xmlns:qa="urn:config.gridrouter.qatools.ru"><browser name="browser"/></qa:browsers>`)
}

func TestParseConfigWithoutNamespace(t *testing.T) {
	testParseConfig(t, `<browsers><browser name="browser"/></browsers>`)
}

func testParseConfig(t *testing.T, xmlConfig string) {
	tmp, err := os.CreateTemp("", "config")
	defer os.Remove(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	_, err = tmp.Write([]byte(xmlConfig))
	if err != nil {
		t.Fatal(err)
	}
	err = tmp.Close()
	if err != nil {
		t.Fatal(err)
	}
	var browsers config.Browsers
	err = readConfig(tmp.Name(), &browsers)

	assert.NoError(t, err)
	assert.Equal(t, browsers.Browsers[0].Name, "browser")
}

func TestConfDirDoesNotExist(t *testing.T) {
	err := loadQuotaFiles("missing-dir")
	assert.Error(t, err)
}

func TestConcurrentReload(t *testing.T) {
	go func() {
		_ = loadQuotaFiles("quota")
	}()
	_ = loadQuotaFiles("quota")
}

func TestReloadRemovesDeletedQuota(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	confLock.Lock()
	saved := quota
	quota = make(map[string]ggrBrowsers)
	confLock.Unlock()
	defer func() {
		confLock.Lock()
		quota = saved
		confLock.Unlock()
	}()

	dir := t.TempDir()
	xml := `<qa:browsers xmlns:qa="urn:config.gridrouter.qatools.ru"><browser name="browser"/></qa:browsers>`
	for _, name := range []string{"alice.xml", "bob.xml"} {
		assert.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(xml), 0o644))
	}
	assert.NoError(t, loadQuotaFiles(dir))
	assert.NoError(t, os.Remove(filepath.Join(dir, "bob.xml")))
	assert.NoError(t, loadQuotaFiles(dir))

	confLock.RLock()
	defer confLock.RUnlock()
	_, alice := quota["alice"]
	_, bob := quota["bob"]
	assert.True(t, alice)
	assert.False(t, bob, "quota of a deleted file must be removed on reload")
}

func TestChoosingAllHosts(t *testing.T) {
	//NOTE: the same weights for all hosts are important!
	hosts := config.Hosts{config.Host{Name: "first", Count: 1}, config.Host{Name: "mid", Count: 1}, config.Host{Name: "last", Count: 1}}
	chosenHosts := make(map[string]int)
	for i := 0; i < 100; i++ {
		host, _ := choose(hosts)
		chosenHosts[host.Name]++
	}
	assert.True(t, chosenHosts["first"] > 0)
	assert.True(t, chosenHosts["mid"] > 0)
	assert.True(t, chosenHosts["last"] > 0)
}
