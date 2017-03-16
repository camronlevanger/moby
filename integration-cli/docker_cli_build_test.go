package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/docker/docker/builder/dockerfile/command"
	"github.com/docker/docker/integration-cli/checker"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/stringutils"
	"github.com/docker/docker/pkg/testutil"
	icmd "github.com/docker/docker/pkg/testutil/cmd"
	"github.com/go-check/check"
)

func (s *DockerSuite) TestBuildJSONEmptyRun(c *check.C) {
	buildImageSuccessfully(c, "testbuildjsonemptyrun", withDockerfile(`
    FROM busybox
    RUN []
    `))
}

func (s *DockerSuite) TestBuildShCmdJSONEntrypoint(c *check.C) {
	name := "testbuildshcmdjsonentrypoint"
	expected := "/bin/sh -c echo test"
	if testEnv.DaemonPlatform() == "windows" {
		expected = "cmd /S /C echo test"
	}

	buildImageSuccessfully(c, name, withDockerfile(`
    FROM busybox
    ENTRYPOINT ["echo"]
    CMD echo test
    `))
	out, _ := dockerCmd(c, "run", "--rm", name)

	if strings.TrimSpace(out) != expected {
		c.Fatalf("CMD did not contain %q : %q", expected, out)
	}
}

func (s *DockerSuite) TestBuildEnvironmentReplacementUser(c *check.C) {
	// Windows does not support FROM scratch or the USER command
	testRequires(c, DaemonIsLinux)
	name := "testbuildenvironmentreplacement"

	buildImageSuccessfully(c, name, withDockerfile(`
  FROM scratch
  ENV user foo
  USER ${user}
  `))
	res := inspectFieldJSON(c, name, "Config.User")

	if res != `"foo"` {
		c.Fatal("User foo from environment not in Config.User on image")
	}
}

func (s *DockerSuite) TestBuildEnvironmentReplacementVolume(c *check.C) {
	name := "testbuildenvironmentreplacement"

	var volumePath string

	if testEnv.DaemonPlatform() == "windows" {
		volumePath = "c:/quux"
	} else {
		volumePath = "/quux"
	}

	buildImageSuccessfully(c, name, withDockerfile(`
  FROM `+minimalBaseImage()+`
  ENV volume `+volumePath+`
  VOLUME ${volume}
  `))

	var volumes map[string]interface{}
	inspectFieldAndUnmarshall(c, name, "Config.Volumes", &volumes)
	if _, ok := volumes[volumePath]; !ok {
		c.Fatal("Volume " + volumePath + " from environment not in Config.Volumes on image")
	}

}

func (s *DockerSuite) TestBuildEnvironmentReplacementExpose(c *check.C) {
	// Windows does not support FROM scratch or the EXPOSE command
	testRequires(c, DaemonIsLinux)
	name := "testbuildenvironmentreplacement"

	buildImageSuccessfully(c, name, withDockerfile(`
  FROM scratch
  ENV port 80
  EXPOSE ${port}
  ENV ports "  99   100 "
  EXPOSE ${ports}
  `))

	var exposedPorts map[string]interface{}
	inspectFieldAndUnmarshall(c, name, "Config.ExposedPorts", &exposedPorts)
	exp := []int{80, 99, 100}
	for _, p := range exp {
		tmp := fmt.Sprintf("%d/tcp", p)
		if _, ok := exposedPorts[tmp]; !ok {
			c.Fatalf("Exposed port %d from environment not in Config.ExposedPorts on image", p)
		}
	}

}

func (s *DockerSuite) TestBuildEnvironmentReplacementWorkdir(c *check.C) {
	name := "testbuildenvironmentreplacement"

	buildImageSuccessfully(c, name, withDockerfile(`
  FROM busybox
  ENV MYWORKDIR /work
  RUN mkdir ${MYWORKDIR}
  WORKDIR ${MYWORKDIR}
  `))
	res := inspectFieldJSON(c, name, "Config.WorkingDir")

	expected := `"/work"`
	if testEnv.DaemonPlatform() == "windows" {
		expected = `"C:\\work"`
	}
	if res != expected {
		c.Fatalf("Workdir /workdir from environment not in Config.WorkingDir on image: %s", res)
	}
}

func (s *DockerSuite) TestBuildEnvironmentReplacementAddCopy(c *check.C) {
	name := "testbuildenvironmentreplacement"

	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", `
  FROM `+minimalBaseImage()+`
  ENV baz foo
  ENV quux bar
  ENV dot .
  ENV fee fff
  ENV gee ggg

  ADD ${baz} ${dot}
  COPY ${quux} ${dot}
  ADD ${zzz:-${fee}} ${dot}
  COPY ${zzz:-${gee}} ${dot}
  `),
		withFile("foo", "test1"),
		withFile("bar", "test2"),
		withFile("fff", "test3"),
		withFile("ggg", "test4"),
	))
}

func (s *DockerSuite) TestBuildEnvironmentReplacementEnv(c *check.C) {
	// ENV expansions work differently in Windows
	testRequires(c, DaemonIsLinux)
	name := "testbuildenvironmentreplacement"

	buildImageSuccessfully(c, name, withDockerfile(`
  FROM busybox
  ENV foo zzz
  ENV bar ${foo}
  ENV abc1='$foo'
  ENV env1=$foo env2=${foo} env3="$foo" env4="${foo}"
  RUN [ "$abc1" = '$foo' ] && (echo "$abc1" | grep -q foo)
  ENV abc2="\$foo"
  RUN [ "$abc2" = '$foo' ] && (echo "$abc2" | grep -q foo)
  ENV abc3 '$foo'
  RUN [ "$abc3" = '$foo' ] && (echo "$abc3" | grep -q foo)
  ENV abc4 "\$foo"
  RUN [ "$abc4" = '$foo' ] && (echo "$abc4" | grep -q foo)
  `))

	envResult := []string{}
	inspectFieldAndUnmarshall(c, name, "Config.Env", &envResult)
	found := false
	envCount := 0

	for _, env := range envResult {
		parts := strings.SplitN(env, "=", 2)
		if parts[0] == "bar" {
			found = true
			if parts[1] != "zzz" {
				c.Fatalf("Could not find replaced var for env `bar`: got %q instead of `zzz`", parts[1])
			}
		} else if strings.HasPrefix(parts[0], "env") {
			envCount++
			if parts[1] != "zzz" {
				c.Fatalf("%s should be 'zzz' but instead its %q", parts[0], parts[1])
			}
		} else if strings.HasPrefix(parts[0], "env") {
			envCount++
			if parts[1] != "foo" {
				c.Fatalf("%s should be 'foo' but instead its %q", parts[0], parts[1])
			}
		}
	}

	if !found {
		c.Fatal("Never found the `bar` env variable")
	}

	if envCount != 4 {
		c.Fatalf("Didn't find all env vars - only saw %d\n%s", envCount, envResult)
	}

}

func (s *DockerSuite) TestBuildHandleEscapesInVolume(c *check.C) {
	// The volume paths used in this test are invalid on Windows
	testRequires(c, DaemonIsLinux)
	name := "testbuildhandleescapes"

	testCases := []struct {
		volumeValue string
		expected    string
	}{
		{
			volumeValue: "${FOO}",
			expected:    "bar",
		},
		{
			volumeValue: `\${FOO}`,
			expected:    "${FOO}",
		},
		// this test in particular provides *7* backslashes and expects 6 to come back.
		// Like above, the first escape is swallowed and the rest are treated as
		// literals, this one is just less obvious because of all the character noise.
		{
			volumeValue: `\\\\\\\${FOO}`,
			expected:    `\\\${FOO}`,
		},
	}

	for _, tc := range testCases {
		buildImageSuccessfully(c, name, withDockerfile(fmt.Sprintf(`
  FROM scratch
  ENV FOO bar
  VOLUME %s
  `, tc.volumeValue)))

		var result map[string]map[string]struct{}
		inspectFieldAndUnmarshall(c, name, "Config.Volumes", &result)
		if _, ok := result[tc.expected]; !ok {
			c.Fatalf("Could not find volume %s set from env foo in volumes table, got %q", tc.expected, result)
		}

		// Remove the image for the next iteration
		dockerCmd(c, "rmi", name)
	}
}

func (s *DockerSuite) TestBuildOnBuildLowercase(c *check.C) {
	name := "testbuildonbuildlowercase"
	name2 := "testbuildonbuildlowercase2"

	buildImageSuccessfully(c, name, withDockerfile(`
  FROM busybox
  onbuild run echo quux
  `))

	result := buildImage(name2, withDockerfile(fmt.Sprintf(`
  FROM %s
  `, name)))
	result.Assert(c, icmd.Success)

	if !strings.Contains(result.Combined(), "quux") {
		c.Fatalf("Did not receive the expected echo text, got %s", result.Combined())
	}

	if strings.Contains(result.Combined(), "ONBUILD ONBUILD") {
		c.Fatalf("Got an ONBUILD ONBUILD error with no error: got %s", result.Combined())
	}

}

func (s *DockerSuite) TestBuildEnvEscapes(c *check.C) {
	// ENV expansions work differently in Windows
	testRequires(c, DaemonIsLinux)
	name := "testbuildenvescapes"
	buildImageSuccessfully(c, name, withDockerfile(`
    FROM busybox
    ENV TEST foo
    CMD echo \$
    `))

	out, _ := dockerCmd(c, "run", "-t", name)
	if strings.TrimSpace(out) != "$" {
		c.Fatalf("Env TEST was not overwritten with bar when foo was supplied to dockerfile: was %q", strings.TrimSpace(out))
	}

}

func (s *DockerSuite) TestBuildEnvOverwrite(c *check.C) {
	// ENV expansions work differently in Windows
	testRequires(c, DaemonIsLinux)
	name := "testbuildenvoverwrite"
	buildImageSuccessfully(c, name, withDockerfile(`
    FROM busybox
    ENV TEST foo
    CMD echo ${TEST}
    `))

	out, _ := dockerCmd(c, "run", "-e", "TEST=bar", "-t", name)
	if strings.TrimSpace(out) != "bar" {
		c.Fatalf("Env TEST was not overwritten with bar when foo was supplied to dockerfile: was %q", strings.TrimSpace(out))
	}

}

// FIXME(vdemeester) why we disabled cache here ?
func (s *DockerSuite) TestBuildOnBuildCmdEntrypointJSON(c *check.C) {
	name1 := "onbuildcmd"
	name2 := "onbuildgenerated"

	buildImageSuccessfully(c, name1, withDockerfile(`
FROM busybox
ONBUILD CMD ["hello world"]
ONBUILD ENTRYPOINT ["echo"]
ONBUILD RUN ["true"]`))

	buildImageSuccessfully(c, name2, withDockerfile(fmt.Sprintf(`FROM %s`, name1)))

	out, _ := dockerCmd(c, "run", name2)
	if !regexp.MustCompile(`(?m)^hello world`).MatchString(out) {
		c.Fatalf("did not get echo output from onbuild. Got: %q", out)
	}

}

// FIXME(vdemeester) why we disabled cache here ?
func (s *DockerSuite) TestBuildOnBuildEntrypointJSON(c *check.C) {
	name1 := "onbuildcmd"
	name2 := "onbuildgenerated"

	buildImageSuccessfully(c, name1, withDockerfile(`
FROM busybox
ONBUILD ENTRYPOINT ["echo"]`))

	buildImageSuccessfully(c, name2, withDockerfile(fmt.Sprintf("FROM %s\nCMD [\"hello world\"]\n", name1)))

	out, _ := dockerCmd(c, "run", name2)
	if !regexp.MustCompile(`(?m)^hello world`).MatchString(out) {
		c.Fatal("got malformed output from onbuild", out)
	}

}

func (s *DockerSuite) TestBuildCacheAdd(c *check.C) {
	testRequires(c, DaemonIsLinux) // Windows doesn't have httpserver image yet
	name := "testbuildtwoimageswithadd"
	server := fakeStorage(c, map[string]string{
		"robots.txt": "hello",
		"index.html": "world",
	})
	defer server.Close()

	buildImageSuccessfully(c, name, withDockerfile(fmt.Sprintf(`FROM scratch
		ADD %s/robots.txt /`, server.URL())))

	result := buildImage(name, withDockerfile(fmt.Sprintf(`FROM scratch
		ADD %s/index.html /`, server.URL())))
	result.Assert(c, icmd.Success)
	if strings.Contains(result.Combined(), "Using cache") {
		c.Fatal("2nd build used cache on ADD, it shouldn't")
	}
}

func (s *DockerSuite) TestBuildLastModified(c *check.C) {
	// Temporary fix for #30890. TODO @jhowardmsft figure out what
	// has changed in the master busybox image.
	testRequires(c, DaemonIsLinux)

	name := "testbuildlastmodified"

	server := fakeStorage(c, map[string]string{
		"file": "hello",
	})
	defer server.Close()

	var out, out2 string

	dFmt := `FROM busybox
ADD %s/file /`
	dockerfile := fmt.Sprintf(dFmt, server.URL())

	buildImageSuccessfully(c, name, withoutCache, withDockerfile(dockerfile))
	out, _ = dockerCmd(c, "run", name, "ls", "-le", "/file")

	// Build it again and make sure the mtime of the file didn't change.
	// Wait a few seconds to make sure the time changed enough to notice
	time.Sleep(2 * time.Second)

	buildImageSuccessfully(c, name, withoutCache, withDockerfile(dockerfile))
	out2, _ = dockerCmd(c, "run", name, "ls", "-le", "/file")

	if out != out2 {
		c.Fatalf("MTime changed:\nOrigin:%s\nNew:%s", out, out2)
	}

	// Now 'touch' the file and make sure the timestamp DID change this time
	// Create a new fakeStorage instead of just using Add() to help windows
	server = fakeStorage(c, map[string]string{
		"file": "hello",
	})
	defer server.Close()

	dockerfile = fmt.Sprintf(dFmt, server.URL())
	buildImageSuccessfully(c, name, withoutCache, withDockerfile(dockerfile))
	out2, _ = dockerCmd(c, "run", name, "ls", "-le", "/file")

	if out == out2 {
		c.Fatalf("MTime didn't change:\nOrigin:%s\nNew:%s", out, out2)
	}

}

// Regression for https://github.com/docker/docker/pull/27805
// Makes sure that we don't use the cache if the contents of
// a file in a subfolder of the context is modified and we re-build.
func (s *DockerSuite) TestBuildModifyFileInFolder(c *check.C) {
	name := "testbuildmodifyfileinfolder"

	ctx := fakeContext(c, `FROM busybox
RUN ["mkdir", "/test"]
ADD folder/file /test/changetarget`,
		map[string]string{})
	defer ctx.Close()
	if err := ctx.Add("folder/file", "first"); err != nil {
		c.Fatal(err)
	}
	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id1 := getIDByName(c, name)
	if err := ctx.Add("folder/file", "second"); err != nil {
		c.Fatal(err)
	}
	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id2 := getIDByName(c, name)
	if id1 == id2 {
		c.Fatal("cache was used even though file contents in folder was changed")
	}
}

func (s *DockerSuite) TestBuildAddSingleFileToRoot(c *check.C) {
	testRequires(c, DaemonIsLinux) // Linux specific test
	buildImageSuccessfully(c, "testaddimg", withBuildContext(c,
		withFile("Dockerfile", fmt.Sprintf(`FROM busybox
RUN echo 'dockerio:x:1001:1001::/bin:/bin/false' >> /etc/passwd
RUN echo 'dockerio:x:1001:' >> /etc/group
RUN touch /exists
RUN chown dockerio.dockerio /exists
ADD test_file /
RUN [ $(ls -l /test_file | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l /test_file | awk '{print $1}') = '%s' ]
RUN [ $(ls -l /exists | awk '{print $3":"$4}') = 'dockerio:dockerio' ]`, expectedFileChmod)),
		withFile("test_file", "test1")))
}

// Issue #3960: "ADD src ." hangs
func (s *DockerSuite) TestBuildAddSingleFileToWorkdir(c *check.C) {
	name := "testaddsinglefiletoworkdir"
	ctx := fakeContext(c, `FROM busybox
ADD test_file .`,
		map[string]string{
			"test_file": "test1",
		})
	defer ctx.Close()

	errChan := make(chan error)
	go func() {
		errChan <- buildImage(name, withExternalBuildContext(ctx)).Error
		close(errChan)
	}()
	select {
	case <-time.After(15 * time.Second):
		c.Fatal("Build with adding to workdir timed out")
	case err := <-errChan:
		c.Assert(err, check.IsNil)
	}
}

func (s *DockerSuite) TestBuildAddSingleFileToExistDir(c *check.C) {
	testRequires(c, DaemonIsLinux) // Linux specific test
	buildImageSuccessfully(c, "testaddsinglefiletoexistdir", withBuildContext(c,
		withFile("Dockerfile", `FROM busybox
RUN echo 'dockerio:x:1001:1001::/bin:/bin/false' >> /etc/passwd
RUN echo 'dockerio:x:1001:' >> /etc/group
RUN mkdir /exists
RUN touch /exists/exists_file
RUN chown -R dockerio.dockerio /exists
ADD test_file /exists/
RUN [ $(ls -l / | grep exists | awk '{print $3":"$4}') = 'dockerio:dockerio' ]
RUN [ $(ls -l /exists/test_file | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l /exists/exists_file | awk '{print $3":"$4}') = 'dockerio:dockerio' ]`),
		withFile("test_file", "test1")))
}

func (s *DockerSuite) TestBuildCopyAddMultipleFiles(c *check.C) {
	testRequires(c, DaemonIsLinux) // Linux specific test
	server := fakeStorage(c, map[string]string{
		"robots.txt": "hello",
	})
	defer server.Close()

	buildImageSuccessfully(c, "testcopymultiplefilestofile", withBuildContext(c,
		withFile("Dockerfile", fmt.Sprintf(`FROM busybox
RUN echo 'dockerio:x:1001:1001::/bin:/bin/false' >> /etc/passwd
RUN echo 'dockerio:x:1001:' >> /etc/group
RUN mkdir /exists
RUN touch /exists/exists_file
RUN chown -R dockerio.dockerio /exists
COPY test_file1 test_file2 /exists/
ADD test_file3 test_file4 %s/robots.txt /exists/
RUN [ $(ls -l / | grep exists | awk '{print $3":"$4}') = 'dockerio:dockerio' ]
RUN [ $(ls -l /exists/test_file1 | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l /exists/test_file2 | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l /exists/test_file3 | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l /exists/test_file4 | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l /exists/robots.txt | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l /exists/exists_file | awk '{print $3":"$4}') = 'dockerio:dockerio' ]
`, server.URL())),
		withFile("test_file1", "test1"),
		withFile("test_file2", "test2"),
		withFile("test_file3", "test3"),
		withFile("test_file3", "test3"),
		withFile("test_file4", "test4")))
}

// These tests are mainly for user namespaces to verify that new directories
// are created as the remapped root uid/gid pair
func (s *DockerSuite) TestBuildUsernamespaceValidateRemappedRoot(c *check.C) {
	testRequires(c, DaemonIsLinux)
	testCases := []string{
		"ADD . /new_dir",
		"COPY test_dir /new_dir",
		"WORKDIR /new_dir",
	}
	name := "testbuildusernamespacevalidateremappedroot"
	for _, tc := range testCases {
		buildImageSuccessfully(c, name, withBuildContext(c,
			withFile("Dockerfile", fmt.Sprintf(`FROM busybox
%s
RUN [ $(ls -l / | grep new_dir | awk '{print $3":"$4}') = 'root:root' ]`, tc)),
			withFile("test_dir/test_file", "test file")))

		dockerCmd(c, "rmi", name)
	}
}

func (s *DockerSuite) TestBuildAddAndCopyFileWithWhitespace(c *check.C) {
	testRequires(c, DaemonIsLinux) // Not currently passing on Windows
	name := "testaddfilewithwhitespace"

	for _, command := range []string{"ADD", "COPY"} {
		buildImageSuccessfully(c, name, withBuildContext(c,
			withFile("Dockerfile", fmt.Sprintf(`FROM busybox
RUN mkdir "/test dir"
RUN mkdir "/test_dir"
%s [ "test file1", "/test_file1" ]
%s [ "test_file2", "/test file2" ]
%s [ "test file3", "/test file3" ]
%s [ "test dir/test_file4", "/test_dir/test_file4" ]
%s [ "test_dir/test_file5", "/test dir/test_file5" ]
%s [ "test dir/test_file6", "/test dir/test_file6" ]
RUN [ $(cat "/test_file1") = 'test1' ]
RUN [ $(cat "/test file2") = 'test2' ]
RUN [ $(cat "/test file3") = 'test3' ]
RUN [ $(cat "/test_dir/test_file4") = 'test4' ]
RUN [ $(cat "/test dir/test_file5") = 'test5' ]
RUN [ $(cat "/test dir/test_file6") = 'test6' ]`, command, command, command, command, command, command)),
			withFile("test file1", "test1"),
			withFile("test_file2", "test2"),
			withFile("test file3", "test3"),
			withFile("test dir/test_file4", "test4"),
			withFile("test_dir/test_file5", "test5"),
			withFile("test dir/test_file6", "test6"),
		))

		dockerCmd(c, "rmi", name)
	}
}

func (s *DockerSuite) TestBuildCopyFileWithWhitespaceOnWindows(c *check.C) {
	testRequires(c, DaemonIsWindows)
	dockerfile := `FROM ` + testEnv.MinimalBaseImage() + `
RUN mkdir "C:/test dir"
RUN mkdir "C:/test_dir"
COPY [ "test file1", "/test_file1" ]
COPY [ "test_file2", "/test file2" ]
COPY [ "test file3", "/test file3" ]
COPY [ "test dir/test_file4", "/test_dir/test_file4" ]
COPY [ "test_dir/test_file5", "/test dir/test_file5" ]
COPY [ "test dir/test_file6", "/test dir/test_file6" ]
RUN find "test1" "C:/test_file1"
RUN find "test2" "C:/test file2"
RUN find "test3" "C:/test file3"
RUN find "test4" "C:/test_dir/test_file4"
RUN find "test5" "C:/test dir/test_file5"
RUN find "test6" "C:/test dir/test_file6"`

	name := "testcopyfilewithwhitespace"
	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", dockerfile),
		withFile("test file1", "test1"),
		withFile("test_file2", "test2"),
		withFile("test file3", "test3"),
		withFile("test dir/test_file4", "test4"),
		withFile("test_dir/test_file5", "test5"),
		withFile("test dir/test_file6", "test6"),
	))
}

func (s *DockerSuite) TestBuildCopyWildcard(c *check.C) {
	name := "testcopywildcard"
	server := fakeStorage(c, map[string]string{
		"robots.txt": "hello",
		"index.html": "world",
	})
	defer server.Close()

	ctx := fakeContext(c, fmt.Sprintf(`FROM busybox
	COPY file*.txt /tmp/
	RUN ls /tmp/file1.txt /tmp/file2.txt
	RUN [ "mkdir",  "/tmp1" ]
	COPY dir* /tmp1/
	RUN ls /tmp1/dirt /tmp1/nested_file /tmp1/nested_dir/nest_nest_file
	RUN [ "mkdir",  "/tmp2" ]
        ADD dir/*dir %s/robots.txt /tmp2/
	RUN ls /tmp2/nest_nest_file /tmp2/robots.txt
	`, server.URL()),
		map[string]string{
			"file1.txt":                     "test1",
			"file2.txt":                     "test2",
			"dir/nested_file":               "nested file",
			"dir/nested_dir/nest_nest_file": "2 times nested",
			"dirt": "dirty",
		})
	defer ctx.Close()

	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id1 := getIDByName(c, name)

	// Now make sure we use a cache the 2nd time
	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id2 := getIDByName(c, name)

	if id1 != id2 {
		c.Fatal("didn't use the cache")
	}

}

func (s *DockerSuite) TestBuildCopyWildcardInName(c *check.C) {
	// Run this only on Linux
	// Below is the original comment (that I don't agree with — vdemeester)
	// Normally we would do c.Fatal(err) here but given that
	// the odds of this failing are so rare, it must be because
	// the OS we're running the client on doesn't support * in
	// filenames (like windows).  So, instead of failing the test
	// just let it pass. Then we don't need to explicitly
	// say which OSs this works on or not.
	testRequires(c, DaemonIsLinux, UnixCli)

	buildImageSuccessfully(c, "testcopywildcardinname", withBuildContext(c,
		withFile("Dockerfile", `FROM busybox
	COPY *.txt /tmp/
	RUN [ "$(cat /tmp/\*.txt)" = 'hi there' ]
	`),
		withFile("*.txt", "hi there"),
	))
}

func (s *DockerSuite) TestBuildCopyWildcardCache(c *check.C) {
	name := "testcopywildcardcache"
	ctx := fakeContext(c, `FROM busybox
	COPY file1.txt /tmp/`,
		map[string]string{
			"file1.txt": "test1",
		})
	defer ctx.Close()

	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id1 := getIDByName(c, name)

	// Now make sure we use a cache the 2nd time even with wild cards.
	// Use the same context so the file is the same and the checksum will match
	ctx.Add("Dockerfile", `FROM busybox
	COPY file*.txt /tmp/`)

	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id2 := getIDByName(c, name)

	if id1 != id2 {
		c.Fatal("didn't use the cache")
	}

}

func (s *DockerSuite) TestBuildAddSingleFileToNonExistingDir(c *check.C) {
	testRequires(c, DaemonIsLinux) // Linux specific test
	buildImageSuccessfully(c, "testaddsinglefiletononexistingdir", withBuildContext(c,
		withFile("Dockerfile", `FROM busybox
RUN echo 'dockerio:x:1001:1001::/bin:/bin/false' >> /etc/passwd
RUN echo 'dockerio:x:1001:' >> /etc/group
RUN touch /exists
RUN chown dockerio.dockerio /exists
ADD test_file /test_dir/
RUN [ $(ls -l / | grep test_dir | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l /test_dir/test_file | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l /exists | awk '{print $3":"$4}') = 'dockerio:dockerio' ]`),
		withFile("test_file", "test1")))
}

func (s *DockerSuite) TestBuildAddDirContentToRoot(c *check.C) {
	testRequires(c, DaemonIsLinux) // Linux specific test
	buildImageSuccessfully(c, "testadddircontenttoroot", withBuildContext(c,
		withFile("Dockerfile", `FROM busybox
RUN echo 'dockerio:x:1001:1001::/bin:/bin/false' >> /etc/passwd
RUN echo 'dockerio:x:1001:' >> /etc/group
RUN touch /exists
RUN chown dockerio.dockerio exists
ADD test_dir /
RUN [ $(ls -l /test_file | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l /exists | awk '{print $3":"$4}') = 'dockerio:dockerio' ]`),
		withFile("test_dir/test_file", "test1")))
}

func (s *DockerSuite) TestBuildAddDirContentToExistingDir(c *check.C) {
	testRequires(c, DaemonIsLinux) // Linux specific test
	buildImageSuccessfully(c, "testadddircontenttoexistingdir", withBuildContext(c,
		withFile("Dockerfile", `FROM busybox
RUN echo 'dockerio:x:1001:1001::/bin:/bin/false' >> /etc/passwd
RUN echo 'dockerio:x:1001:' >> /etc/group
RUN mkdir /exists
RUN touch /exists/exists_file
RUN chown -R dockerio.dockerio /exists
ADD test_dir/ /exists/
RUN [ $(ls -l / | grep exists | awk '{print $3":"$4}') = 'dockerio:dockerio' ]
RUN [ $(ls -l /exists/exists_file | awk '{print $3":"$4}') = 'dockerio:dockerio' ]
RUN [ $(ls -l /exists/test_file | awk '{print $3":"$4}') = 'root:root' ]`),
		withFile("test_dir/test_file", "test1")))
}

func (s *DockerSuite) TestBuildAddWholeDirToRoot(c *check.C) {
	testRequires(c, DaemonIsLinux) // Linux specific test
	buildImageSuccessfully(c, "testaddwholedirtoroot", withBuildContext(c,
		withFile("Dockerfile", fmt.Sprintf(`FROM busybox
RUN echo 'dockerio:x:1001:1001::/bin:/bin/false' >> /etc/passwd
RUN echo 'dockerio:x:1001:' >> /etc/group
RUN touch /exists
RUN chown dockerio.dockerio exists
ADD test_dir /test_dir
RUN [ $(ls -l / | grep test_dir | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l / | grep test_dir | awk '{print $1}') = 'drwxr-xr-x' ]
RUN [ $(ls -l /test_dir/test_file | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l /test_dir/test_file | awk '{print $1}') = '%s' ]
RUN [ $(ls -l /exists | awk '{print $3":"$4}') = 'dockerio:dockerio' ]`, expectedFileChmod)),
		withFile("test_dir/test_file", "test1")))
}

// Testing #5941 : Having an etc directory in context conflicts with the /etc/mtab
func (s *DockerSuite) TestBuildAddOrCopyEtcToRootShouldNotConflict(c *check.C) {
	buildImageSuccessfully(c, "testaddetctoroot", withBuildContext(c,
		withFile("Dockerfile", `FROM `+minimalBaseImage()+`
ADD . /`),
		withFile("etc/test_file", "test1")))
	buildImageSuccessfully(c, "testcopyetctoroot", withBuildContext(c,
		withFile("Dockerfile", `FROM `+minimalBaseImage()+`
COPY . /`),
		withFile("etc/test_file", "test1")))
}

// Testing #9401 : Losing setuid flag after a ADD
func (s *DockerSuite) TestBuildAddPreservesFilesSpecialBits(c *check.C) {
	testRequires(c, DaemonIsLinux) // Linux specific test
	buildImageSuccessfully(c, "testaddetctoroot", withBuildContext(c,
		withFile("Dockerfile", `FROM busybox
ADD suidbin /usr/bin/suidbin
RUN chmod 4755 /usr/bin/suidbin
RUN [ $(ls -l /usr/bin/suidbin | awk '{print $1}') = '-rwsr-xr-x' ]
ADD ./data/ /
RUN [ $(ls -l /usr/bin/suidbin | awk '{print $1}') = '-rwsr-xr-x' ]`),
		withFile("suidbin", "suidbin"),
		withFile("/data/usr/test_file", "test1")))
}

func (s *DockerSuite) TestBuildCopySingleFileToRoot(c *check.C) {
	testRequires(c, DaemonIsLinux) // Linux specific test
	buildImageSuccessfully(c, "testcopysinglefiletoroot", withBuildContext(c,
		withFile("Dockerfile", fmt.Sprintf(`FROM busybox
RUN echo 'dockerio:x:1001:1001::/bin:/bin/false' >> /etc/passwd
RUN echo 'dockerio:x:1001:' >> /etc/group
RUN touch /exists
RUN chown dockerio.dockerio /exists
COPY test_file /
RUN [ $(ls -l /test_file | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l /test_file | awk '{print $1}') = '%s' ]
RUN [ $(ls -l /exists | awk '{print $3":"$4}') = 'dockerio:dockerio' ]`, expectedFileChmod)),
		withFile("test_file", "test1")))
}

// Issue #3960: "ADD src ." hangs - adapted for COPY
func (s *DockerSuite) TestBuildCopySingleFileToWorkdir(c *check.C) {
	name := "testcopysinglefiletoworkdir"
	ctx := fakeContext(c, `FROM busybox
COPY test_file .`,
		map[string]string{
			"test_file": "test1",
		})
	defer ctx.Close()

	errChan := make(chan error)
	go func() {
		errChan <- buildImage(name, withExternalBuildContext(ctx)).Error
		close(errChan)
	}()
	select {
	case <-time.After(15 * time.Second):
		c.Fatal("Build with adding to workdir timed out")
	case err := <-errChan:
		c.Assert(err, check.IsNil)
	}
}

func (s *DockerSuite) TestBuildCopySingleFileToExistDir(c *check.C) {
	testRequires(c, DaemonIsLinux) // Linux specific test
	buildImageSuccessfully(c, "testcopysinglefiletoexistdir", withBuildContext(c,
		withFile("Dockerfile", `FROM busybox
RUN echo 'dockerio:x:1001:1001::/bin:/bin/false' >> /etc/passwd
RUN echo 'dockerio:x:1001:' >> /etc/group
RUN mkdir /exists
RUN touch /exists/exists_file
RUN chown -R dockerio.dockerio /exists
COPY test_file /exists/
RUN [ $(ls -l / | grep exists | awk '{print $3":"$4}') = 'dockerio:dockerio' ]
RUN [ $(ls -l /exists/test_file | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l /exists/exists_file | awk '{print $3":"$4}') = 'dockerio:dockerio' ]`),
		withFile("test_file", "test1")))
}

func (s *DockerSuite) TestBuildCopySingleFileToNonExistDir(c *check.C) {
	testRequires(c, DaemonIsLinux) // Linux specific
	buildImageSuccessfully(c, "testcopysinglefiletononexistdir", withBuildContext(c,
		withFile("Dockerfile", `FROM busybox
RUN echo 'dockerio:x:1001:1001::/bin:/bin/false' >> /etc/passwd
RUN echo 'dockerio:x:1001:' >> /etc/group
RUN touch /exists
RUN chown dockerio.dockerio /exists
COPY test_file /test_dir/
RUN [ $(ls -l / | grep test_dir | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l /test_dir/test_file | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l /exists | awk '{print $3":"$4}') = 'dockerio:dockerio' ]`),
		withFile("test_file", "test1")))
}

func (s *DockerSuite) TestBuildCopyDirContentToRoot(c *check.C) {
	testRequires(c, DaemonIsLinux) // Linux specific test
	buildImageSuccessfully(c, "testcopydircontenttoroot", withBuildContext(c,
		withFile("Dockerfile", `FROM busybox
RUN echo 'dockerio:x:1001:1001::/bin:/bin/false' >> /etc/passwd
RUN echo 'dockerio:x:1001:' >> /etc/group
RUN touch /exists
RUN chown dockerio.dockerio exists
COPY test_dir /
RUN [ $(ls -l /test_file | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l /exists | awk '{print $3":"$4}') = 'dockerio:dockerio' ]`),
		withFile("test_dir/test_file", "test1")))
}

func (s *DockerSuite) TestBuildCopyDirContentToExistDir(c *check.C) {
	testRequires(c, DaemonIsLinux) // Linux specific test
	buildImageSuccessfully(c, "testcopydircontenttoexistdir", withBuildContext(c,
		withFile("Dockerfile", `FROM busybox
RUN echo 'dockerio:x:1001:1001::/bin:/bin/false' >> /etc/passwd
RUN echo 'dockerio:x:1001:' >> /etc/group
RUN mkdir /exists
RUN touch /exists/exists_file
RUN chown -R dockerio.dockerio /exists
COPY test_dir/ /exists/
RUN [ $(ls -l / | grep exists | awk '{print $3":"$4}') = 'dockerio:dockerio' ]
RUN [ $(ls -l /exists/exists_file | awk '{print $3":"$4}') = 'dockerio:dockerio' ]
RUN [ $(ls -l /exists/test_file | awk '{print $3":"$4}') = 'root:root' ]`),
		withFile("test_dir/test_file", "test1")))
}

func (s *DockerSuite) TestBuildCopyWholeDirToRoot(c *check.C) {
	testRequires(c, DaemonIsLinux) // Linux specific test
	buildImageSuccessfully(c, "testcopywholedirtoroot", withBuildContext(c,
		withFile("Dockerfile", fmt.Sprintf(`FROM busybox
RUN echo 'dockerio:x:1001:1001::/bin:/bin/false' >> /etc/passwd
RUN echo 'dockerio:x:1001:' >> /etc/group
RUN touch /exists
RUN chown dockerio.dockerio exists
COPY test_dir /test_dir
RUN [ $(ls -l / | grep test_dir | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l / | grep test_dir | awk '{print $1}') = 'drwxr-xr-x' ]
RUN [ $(ls -l /test_dir/test_file | awk '{print $3":"$4}') = 'root:root' ]
RUN [ $(ls -l /test_dir/test_file | awk '{print $1}') = '%s' ]
RUN [ $(ls -l /exists | awk '{print $3":"$4}') = 'dockerio:dockerio' ]`, expectedFileChmod)),
		withFile("test_dir/test_file", "test1")))
}

func (s *DockerSuite) TestBuildAddBadLinks(c *check.C) {
	testRequires(c, DaemonIsLinux) // Not currently working on Windows

	dockerfile := `
		FROM scratch
		ADD links.tar /
		ADD foo.txt /symlink/
		`
	targetFile := "foo.txt"
	var (
		name = "test-link-absolute"
	)
	ctx := fakeContext(c, dockerfile, nil)
	defer ctx.Close()

	tempDir, err := ioutil.TempDir("", "test-link-absolute-temp-")
	if err != nil {
		c.Fatalf("failed to create temporary directory: %s", tempDir)
	}
	defer os.RemoveAll(tempDir)

	var symlinkTarget string
	if runtime.GOOS == "windows" {
		var driveLetter string
		if abs, err := filepath.Abs(tempDir); err != nil {
			c.Fatal(err)
		} else {
			driveLetter = abs[:1]
		}
		tempDirWithoutDrive := tempDir[2:]
		symlinkTarget = fmt.Sprintf(`%s:\..\..\..\..\..\..\..\..\..\..\..\..%s`, driveLetter, tempDirWithoutDrive)
	} else {
		symlinkTarget = fmt.Sprintf("/../../../../../../../../../../../..%s", tempDir)
	}

	tarPath := filepath.Join(ctx.Dir, "links.tar")
	nonExistingFile := filepath.Join(tempDir, targetFile)
	fooPath := filepath.Join(ctx.Dir, targetFile)

	tarOut, err := os.Create(tarPath)
	if err != nil {
		c.Fatal(err)
	}

	tarWriter := tar.NewWriter(tarOut)

	header := &tar.Header{
		Name:     "symlink",
		Typeflag: tar.TypeSymlink,
		Linkname: symlinkTarget,
		Mode:     0755,
		Uid:      0,
		Gid:      0,
	}

	err = tarWriter.WriteHeader(header)
	if err != nil {
		c.Fatal(err)
	}

	tarWriter.Close()
	tarOut.Close()

	foo, err := os.Create(fooPath)
	if err != nil {
		c.Fatal(err)
	}
	defer foo.Close()

	if _, err := foo.WriteString("test"); err != nil {
		c.Fatal(err)
	}

	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	if _, err := os.Stat(nonExistingFile); err == nil || err != nil && !os.IsNotExist(err) {
		c.Fatalf("%s shouldn't have been written and it shouldn't exist", nonExistingFile)
	}

}

func (s *DockerSuite) TestBuildAddBadLinksVolume(c *check.C) {
	testRequires(c, DaemonIsLinux) // ln not implemented on Windows busybox
	const (
		dockerfileTemplate = `
		FROM busybox
		RUN ln -s /../../../../../../../../%s /x
		VOLUME /x
		ADD foo.txt /x/`
		targetFile = "foo.txt"
	)
	var (
		name       = "test-link-absolute-volume"
		dockerfile = ""
	)

	tempDir, err := ioutil.TempDir("", "test-link-absolute-volume-temp-")
	if err != nil {
		c.Fatalf("failed to create temporary directory: %s", tempDir)
	}
	defer os.RemoveAll(tempDir)

	dockerfile = fmt.Sprintf(dockerfileTemplate, tempDir)
	nonExistingFile := filepath.Join(tempDir, targetFile)

	ctx := fakeContext(c, dockerfile, nil)
	defer ctx.Close()
	fooPath := filepath.Join(ctx.Dir, targetFile)

	foo, err := os.Create(fooPath)
	if err != nil {
		c.Fatal(err)
	}
	defer foo.Close()

	if _, err := foo.WriteString("test"); err != nil {
		c.Fatal(err)
	}

	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	if _, err := os.Stat(nonExistingFile); err == nil || err != nil && !os.IsNotExist(err) {
		c.Fatalf("%s shouldn't have been written and it shouldn't exist", nonExistingFile)
	}

}

// Issue #5270 - ensure we throw a better error than "unexpected EOF"
// when we can't access files in the context.
func (s *DockerSuite) TestBuildWithInaccessibleFilesInContext(c *check.C) {
	testRequires(c, DaemonIsLinux, UnixCli) // test uses chown/chmod: not available on windows

	{
		name := "testbuildinaccessiblefiles"
		ctx := fakeContext(c, "FROM scratch\nADD . /foo/", map[string]string{"fileWithoutReadAccess": "foo"})
		defer ctx.Close()
		// This is used to ensure we detect inaccessible files early during build in the cli client
		pathToFileWithoutReadAccess := filepath.Join(ctx.Dir, "fileWithoutReadAccess")

		if err := os.Chown(pathToFileWithoutReadAccess, 0, 0); err != nil {
			c.Fatalf("failed to chown file to root: %s", err)
		}
		if err := os.Chmod(pathToFileWithoutReadAccess, 0700); err != nil {
			c.Fatalf("failed to chmod file to 700: %s", err)
		}
		result := icmd.RunCmd(icmd.Cmd{
			Command: []string{"su", "unprivilegeduser", "-c", fmt.Sprintf("%s build -t %s .", dockerBinary, name)},
			Dir:     ctx.Dir,
		})
		if result.Error == nil {
			c.Fatalf("build should have failed: %s %s", result.Error, result.Combined())
		}

		// check if we've detected the failure before we started building
		if !strings.Contains(result.Combined(), "no permission to read from ") {
			c.Fatalf("output should've contained the string: no permission to read from but contained: %s", result.Combined())
		}

		if !strings.Contains(result.Combined(), "Error checking context") {
			c.Fatalf("output should've contained the string: Error checking context")
		}
	}
	{
		name := "testbuildinaccessibledirectory"
		ctx := fakeContext(c, "FROM scratch\nADD . /foo/", map[string]string{"directoryWeCantStat/bar": "foo"})
		defer ctx.Close()
		// This is used to ensure we detect inaccessible directories early during build in the cli client
		pathToDirectoryWithoutReadAccess := filepath.Join(ctx.Dir, "directoryWeCantStat")
		pathToFileInDirectoryWithoutReadAccess := filepath.Join(pathToDirectoryWithoutReadAccess, "bar")

		if err := os.Chown(pathToDirectoryWithoutReadAccess, 0, 0); err != nil {
			c.Fatalf("failed to chown directory to root: %s", err)
		}
		if err := os.Chmod(pathToDirectoryWithoutReadAccess, 0444); err != nil {
			c.Fatalf("failed to chmod directory to 444: %s", err)
		}
		if err := os.Chmod(pathToFileInDirectoryWithoutReadAccess, 0700); err != nil {
			c.Fatalf("failed to chmod file to 700: %s", err)
		}

		result := icmd.RunCmd(icmd.Cmd{
			Command: []string{"su", "unprivilegeduser", "-c", fmt.Sprintf("%s build -t %s .", dockerBinary, name)},
			Dir:     ctx.Dir,
		})
		if result.Error == nil {
			c.Fatalf("build should have failed: %s %s", result.Error, result.Combined())
		}

		// check if we've detected the failure before we started building
		if !strings.Contains(result.Combined(), "can't stat") {
			c.Fatalf("output should've contained the string: can't access %s", result.Combined())
		}

		if !strings.Contains(result.Combined(), "Error checking context") {
			c.Fatalf("output should've contained the string: Error checking context\ngot:%s", result.Combined())
		}

	}
	{
		name := "testlinksok"
		ctx := fakeContext(c, "FROM scratch\nADD . /foo/", nil)
		defer ctx.Close()

		target := "../../../../../../../../../../../../../../../../../../../azA"
		if err := os.Symlink(filepath.Join(ctx.Dir, "g"), target); err != nil {
			c.Fatal(err)
		}
		defer os.Remove(target)
		// This is used to ensure we don't follow links when checking if everything in the context is accessible
		// This test doesn't require that we run commands as an unprivileged user
		buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	}
	{
		name := "testbuildignoredinaccessible"
		ctx := fakeContext(c, "FROM scratch\nADD . /foo/",
			map[string]string{
				"directoryWeCantStat/bar": "foo",
				".dockerignore":           "directoryWeCantStat",
			})
		defer ctx.Close()
		// This is used to ensure we don't try to add inaccessible files when they are ignored by a .dockerignore pattern
		pathToDirectoryWithoutReadAccess := filepath.Join(ctx.Dir, "directoryWeCantStat")
		pathToFileInDirectoryWithoutReadAccess := filepath.Join(pathToDirectoryWithoutReadAccess, "bar")
		if err := os.Chown(pathToDirectoryWithoutReadAccess, 0, 0); err != nil {
			c.Fatalf("failed to chown directory to root: %s", err)
		}
		if err := os.Chmod(pathToDirectoryWithoutReadAccess, 0444); err != nil {
			c.Fatalf("failed to chmod directory to 444: %s", err)
		}
		if err := os.Chmod(pathToFileInDirectoryWithoutReadAccess, 0700); err != nil {
			c.Fatalf("failed to chmod file to 700: %s", err)
		}

		result := icmd.RunCmd(icmd.Cmd{
			Dir: ctx.Dir,
			Command: []string{"su", "unprivilegeduser", "-c",
				fmt.Sprintf("%s build -t %s .", dockerBinary, name)},
		})
		result.Assert(c, icmd.Expected{})
	}
}

func (s *DockerSuite) TestBuildForceRm(c *check.C) {
	containerCountBefore := getContainerCount(c)
	name := "testbuildforcerm"

	buildImage(name, withBuildFlags("--force-rm"), withBuildContext(c,
		withFile("Dockerfile", `FROM `+minimalBaseImage()+`
	RUN true
	RUN thiswillfail`))).Assert(c, icmd.Expected{
		ExitCode: 1,
	})

	containerCountAfter := getContainerCount(c)
	if containerCountBefore != containerCountAfter {
		c.Fatalf("--force-rm shouldn't have left containers behind")
	}

}

func (s *DockerSuite) TestBuildRm(c *check.C) {
	name := "testbuildrm"

	testCases := []struct {
		buildflags                []string
		shouldLeftContainerBehind bool
	}{
		// Default case (i.e. --rm=true)
		{
			buildflags:                []string{},
			shouldLeftContainerBehind: false,
		},
		{
			buildflags:                []string{"--rm"},
			shouldLeftContainerBehind: false,
		},
		{
			buildflags:                []string{"--rm=false"},
			shouldLeftContainerBehind: true,
		},
	}

	for _, tc := range testCases {
		containerCountBefore := getContainerCount(c)

		buildImageSuccessfully(c, name, withBuildFlags(tc.buildflags...), withDockerfile(`FROM busybox
	RUN echo hello world`))

		containerCountAfter := getContainerCount(c)
		if tc.shouldLeftContainerBehind {
			if containerCountBefore == containerCountAfter {
				c.Fatalf("flags %v should have left containers behind", tc.buildflags)
			}
		} else {
			if containerCountBefore != containerCountAfter {
				c.Fatalf("flags %v shouldn't have left containers behind", tc.buildflags)
			}
		}

		dockerCmd(c, "rmi", name)
	}
}

func (s *DockerSuite) TestBuildWithVolumes(c *check.C) {
	testRequires(c, DaemonIsLinux) // Invalid volume paths on Windows
	var (
		result   map[string]map[string]struct{}
		name     = "testbuildvolumes"
		emptyMap = make(map[string]struct{})
		expected = map[string]map[string]struct{}{
			"/test1":  emptyMap,
			"/test2":  emptyMap,
			"/test3":  emptyMap,
			"/test4":  emptyMap,
			"/test5":  emptyMap,
			"/test6":  emptyMap,
			"[/test7": emptyMap,
			"/test8]": emptyMap,
		}
	)

	buildImageSuccessfully(c, name, withDockerfile(`FROM scratch
		VOLUME /test1
		VOLUME /test2
    VOLUME /test3 /test4
    VOLUME ["/test5", "/test6"]
    VOLUME [/test7 /test8]
    `))

	inspectFieldAndUnmarshall(c, name, "Config.Volumes", &result)

	equal := reflect.DeepEqual(&result, &expected)
	if !equal {
		c.Fatalf("Volumes %s, expected %s", result, expected)
	}

}

func (s *DockerSuite) TestBuildMaintainer(c *check.C) {
	name := "testbuildmaintainer"

	buildImageSuccessfully(c, name, withDockerfile(`FROM `+minimalBaseImage()+`
        MAINTAINER dockerio`))

	expected := "dockerio"
	res := inspectField(c, name, "Author")
	if res != expected {
		c.Fatalf("Maintainer %s, expected %s", res, expected)
	}
}

func (s *DockerSuite) TestBuildUser(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuilduser"
	expected := "dockerio"
	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
		RUN echo 'dockerio:x:1001:1001::/bin:/bin/false' >> /etc/passwd
		USER dockerio
		RUN [ $(whoami) = 'dockerio' ]`))
	res := inspectField(c, name, "Config.User")
	if res != expected {
		c.Fatalf("User %s, expected %s", res, expected)
	}
}

func (s *DockerSuite) TestBuildRelativeWorkdir(c *check.C) {
	name := "testbuildrelativeworkdir"

	var (
		expected1     string
		expected2     string
		expected3     string
		expected4     string
		expectedFinal string
	)

	if testEnv.DaemonPlatform() == "windows" {
		expected1 = `C:/`
		expected2 = `C:/test1`
		expected3 = `C:/test2`
		expected4 = `C:/test2/test3`
		expectedFinal = `C:\test2\test3` // Note inspect is going to return Windows paths, as it's not in busybox
	} else {
		expected1 = `/`
		expected2 = `/test1`
		expected3 = `/test2`
		expected4 = `/test2/test3`
		expectedFinal = `/test2/test3`
	}

	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
		RUN sh -c "[ "$PWD" = "`+expected1+`" ]"
		WORKDIR test1
		RUN sh -c "[ "$PWD" = "`+expected2+`" ]"
		WORKDIR /test2
		RUN sh -c "[ "$PWD" = "`+expected3+`" ]"
		WORKDIR test3
		RUN sh -c "[ "$PWD" = "`+expected4+`" ]"`))

	res := inspectField(c, name, "Config.WorkingDir")
	if res != expectedFinal {
		c.Fatalf("Workdir %s, expected %s", res, expectedFinal)
	}
}

// #22181 Regression test. Single end-to-end test of using
// Windows semantics. Most path handling verifications are in unit tests
func (s *DockerSuite) TestBuildWindowsWorkdirProcessing(c *check.C) {
	testRequires(c, DaemonIsWindows)
	buildImageSuccessfully(c, "testbuildwindowsworkdirprocessing", withDockerfile(`FROM busybox
		WORKDIR C:\\foo
		WORKDIR bar
		RUN sh -c "[ "$PWD" = "C:/foo/bar" ]"
		`))
}

// #22181 Regression test. Most paths handling verifications are in unit test.
// One functional test for end-to-end
func (s *DockerSuite) TestBuildWindowsAddCopyPathProcessing(c *check.C) {
	testRequires(c, DaemonIsWindows)
	// TODO Windows (@jhowardmsft). Needs a follow-up PR to 22181 to
	// support backslash such as .\\ being equivalent to ./ and c:\\ being
	// equivalent to c:/. This is not currently (nor ever has been) supported
	// by docker on the Windows platform.
	buildImageSuccessfully(c, "testbuildwindowsaddcopypathprocessing", withBuildContext(c,
		withFile("Dockerfile", `FROM busybox
			# No trailing slash on COPY/ADD
			# Results in dir being changed to a file
			WORKDIR /wc1
			COPY wc1 c:/wc1
			WORKDIR /wc2
			ADD wc2 c:/wc2
			WORKDIR c:/
			RUN sh -c "[ $(cat c:/wc1/wc1) = 'hellowc1' ]"
			RUN sh -c "[ $(cat c:/wc2/wc2) = 'worldwc2' ]"

			# Trailing slash on COPY/ADD, Windows-style path.
			WORKDIR /wd1
			COPY wd1 c:/wd1/
			WORKDIR /wd2
			ADD wd2 c:/wd2/
			RUN sh -c "[ $(cat c:/wd1/wd1) = 'hellowd1' ]"
			RUN sh -c "[ $(cat c:/wd2/wd2) = 'worldwd2' ]"
			`),
		withFile("wc1", "hellowc1"),
		withFile("wc2", "worldwc2"),
		withFile("wd1", "hellowd1"),
		withFile("wd2", "worldwd2"),
	))
}

func (s *DockerSuite) TestBuildWorkdirWithEnvVariables(c *check.C) {
	name := "testbuildworkdirwithenvvariables"

	var expected string
	if testEnv.DaemonPlatform() == "windows" {
		expected = `C:\test1\test2`
	} else {
		expected = `/test1/test2`
	}

	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
		ENV DIRPATH /test1
		ENV SUBDIRNAME test2
		WORKDIR $DIRPATH
		WORKDIR $SUBDIRNAME/$MISSING_VAR`))
	res := inspectField(c, name, "Config.WorkingDir")
	if res != expected {
		c.Fatalf("Workdir %s, expected %s", res, expected)
	}
}

func (s *DockerSuite) TestBuildRelativeCopy(c *check.C) {
	// cat /test1/test2/foo gets permission denied for the user
	testRequires(c, NotUserNamespace)

	var expected string
	if testEnv.DaemonPlatform() == "windows" {
		expected = `C:/test1/test2`
	} else {
		expected = `/test1/test2`
	}

	buildImageSuccessfully(c, "testbuildrelativecopy", withBuildContext(c,
		withFile("Dockerfile", `FROM busybox
			WORKDIR /test1
			WORKDIR test2
			RUN sh -c "[ "$PWD" = '`+expected+`' ]"
			COPY foo ./
			RUN sh -c "[ $(cat /test1/test2/foo) = 'hello' ]"
			ADD foo ./bar/baz
			RUN sh -c "[ $(cat /test1/test2/bar/baz) = 'hello' ]"
			COPY foo ./bar/baz2
			RUN sh -c "[ $(cat /test1/test2/bar/baz2) = 'hello' ]"
			WORKDIR ..
			COPY foo ./
			RUN sh -c "[ $(cat /test1/foo) = 'hello' ]"
			COPY foo /test3/
			RUN sh -c "[ $(cat /test3/foo) = 'hello' ]"
			WORKDIR /test4
			COPY . .
			RUN sh -c "[ $(cat /test4/foo) = 'hello' ]"
			WORKDIR /test5/test6
			COPY foo ../
			RUN sh -c "[ $(cat /test5/foo) = 'hello' ]"
			`),
		withFile("foo", "hello"),
	))
}

func (s *DockerSuite) TestBuildBlankName(c *check.C) {
	name := "testbuildblankname"
	testCases := []struct {
		expression     string
		expectedStderr string
	}{
		{
			expression:     "ENV =",
			expectedStderr: "ENV names can not be blank",
		},
		{
			expression:     "LABEL =",
			expectedStderr: "LABEL names can not be blank",
		},
		{
			expression:     "ARG =foo",
			expectedStderr: "ARG names can not be blank",
		},
	}

	for _, tc := range testCases {
		buildImage(name, withDockerfile(fmt.Sprintf(`FROM busybox
		%s`, tc.expression))).Assert(c, icmd.Expected{
			ExitCode: 1,
			Err:      tc.expectedStderr,
		})
	}
}

func (s *DockerSuite) TestBuildEnv(c *check.C) {
	testRequires(c, DaemonIsLinux) // ENV expansion is different in Windows
	name := "testbuildenv"
	expected := "[PATH=/test:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin PORT=2375]"
	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
		ENV PATH /test:$PATH
		ENV PORT 2375
		RUN [ $(env | grep PORT) = 'PORT=2375' ]`))
	res := inspectField(c, name, "Config.Env")
	if res != expected {
		c.Fatalf("Env %s, expected %s", res, expected)
	}
}

func (s *DockerSuite) TestBuildPATH(c *check.C) {
	testRequires(c, DaemonIsLinux) // ENV expansion is different in Windows

	defPath := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

	fn := func(dockerfile string, expected string) {
		buildImageSuccessfully(c, "testbldpath", withDockerfile(dockerfile))
		res := inspectField(c, "testbldpath", "Config.Env")
		if res != expected {
			c.Fatalf("Env %q, expected %q for dockerfile:%q", res, expected, dockerfile)
		}
	}

	tests := []struct{ dockerfile, exp string }{
		{"FROM scratch\nMAINTAINER me", "[PATH=" + defPath + "]"},
		{"FROM busybox\nMAINTAINER me", "[PATH=" + defPath + "]"},
		{"FROM scratch\nENV FOO=bar", "[PATH=" + defPath + " FOO=bar]"},
		{"FROM busybox\nENV FOO=bar", "[PATH=" + defPath + " FOO=bar]"},
		{"FROM scratch\nENV PATH=/test", "[PATH=/test]"},
		{"FROM busybox\nENV PATH=/test", "[PATH=/test]"},
		{"FROM scratch\nENV PATH=''", "[PATH=]"},
		{"FROM busybox\nENV PATH=''", "[PATH=]"},
	}

	for _, test := range tests {
		fn(test.dockerfile, test.exp)
	}
}

func (s *DockerSuite) TestBuildContextCleanup(c *check.C) {
	testRequires(c, SameHostDaemon)

	name := "testbuildcontextcleanup"
	entries, err := ioutil.ReadDir(filepath.Join(testEnv.DockerBasePath(), "tmp"))
	if err != nil {
		c.Fatalf("failed to list contents of tmp dir: %s", err)
	}

	buildImageSuccessfully(c, name, withDockerfile(`FROM `+minimalBaseImage()+`
        ENTRYPOINT ["/bin/echo"]`))

	entriesFinal, err := ioutil.ReadDir(filepath.Join(testEnv.DockerBasePath(), "tmp"))
	if err != nil {
		c.Fatalf("failed to list contents of tmp dir: %s", err)
	}
	if err = testutil.CompareDirectoryEntries(entries, entriesFinal); err != nil {
		c.Fatalf("context should have been deleted, but wasn't")
	}

}

func (s *DockerSuite) TestBuildContextCleanupFailedBuild(c *check.C) {
	testRequires(c, SameHostDaemon)

	name := "testbuildcontextcleanup"
	entries, err := ioutil.ReadDir(filepath.Join(testEnv.DockerBasePath(), "tmp"))
	if err != nil {
		c.Fatalf("failed to list contents of tmp dir: %s", err)
	}

	buildImage(name, withDockerfile(`FROM `+minimalBaseImage()+`
	RUN /non/existing/command`)).Assert(c, icmd.Expected{
		ExitCode: 1,
	})

	entriesFinal, err := ioutil.ReadDir(filepath.Join(testEnv.DockerBasePath(), "tmp"))
	if err != nil {
		c.Fatalf("failed to list contents of tmp dir: %s", err)
	}
	if err = testutil.CompareDirectoryEntries(entries, entriesFinal); err != nil {
		c.Fatalf("context should have been deleted, but wasn't")
	}

}

func (s *DockerSuite) TestBuildCmd(c *check.C) {
	name := "testbuildcmd"
	expected := "[/bin/echo Hello World]"

	buildImageSuccessfully(c, name, withDockerfile(`FROM `+minimalBaseImage()+`
        CMD ["/bin/echo", "Hello World"]`))

	res := inspectField(c, name, "Config.Cmd")
	if res != expected {
		c.Fatalf("Cmd %s, expected %s", res, expected)
	}
}

func (s *DockerSuite) TestBuildExpose(c *check.C) {
	testRequires(c, DaemonIsLinux) // Expose not implemented on Windows
	name := "testbuildexpose"
	expected := "map[2375/tcp:{}]"

	buildImageSuccessfully(c, name, withDockerfile(`FROM scratch
        EXPOSE 2375`))

	res := inspectField(c, name, "Config.ExposedPorts")
	if res != expected {
		c.Fatalf("Exposed ports %s, expected %s", res, expected)
	}
}

func (s *DockerSuite) TestBuildExposeMorePorts(c *check.C) {
	testRequires(c, DaemonIsLinux) // Expose not implemented on Windows
	// start building docker file with a large number of ports
	portList := make([]string, 50)
	line := make([]string, 100)
	expectedPorts := make([]int, len(portList)*len(line))
	for i := 0; i < len(portList); i++ {
		for j := 0; j < len(line); j++ {
			p := i*len(line) + j + 1
			line[j] = strconv.Itoa(p)
			expectedPorts[p-1] = p
		}
		if i == len(portList)-1 {
			portList[i] = strings.Join(line, " ")
		} else {
			portList[i] = strings.Join(line, " ") + ` \`
		}
	}

	dockerfile := `FROM scratch
	EXPOSE {{range .}} {{.}}
	{{end}}`
	tmpl := template.Must(template.New("dockerfile").Parse(dockerfile))
	buf := bytes.NewBuffer(nil)
	tmpl.Execute(buf, portList)

	name := "testbuildexpose"
	buildImageSuccessfully(c, name, withDockerfile(buf.String()))

	// check if all the ports are saved inside Config.ExposedPorts
	res := inspectFieldJSON(c, name, "Config.ExposedPorts")
	var exposedPorts map[string]interface{}
	if err := json.Unmarshal([]byte(res), &exposedPorts); err != nil {
		c.Fatal(err)
	}

	for _, p := range expectedPorts {
		ep := fmt.Sprintf("%d/tcp", p)
		if _, ok := exposedPorts[ep]; !ok {
			c.Errorf("Port(%s) is not exposed", ep)
		} else {
			delete(exposedPorts, ep)
		}
	}
	if len(exposedPorts) != 0 {
		c.Errorf("Unexpected extra exposed ports %v", exposedPorts)
	}
}

func (s *DockerSuite) TestBuildExposeOrder(c *check.C) {
	testRequires(c, DaemonIsLinux) // Expose not implemented on Windows
	buildID := func(name, exposed string) string {
		buildImageSuccessfully(c, name, withDockerfile(fmt.Sprintf(`FROM scratch
		EXPOSE %s`, exposed)))
		id := inspectField(c, name, "Id")
		return id
	}

	id1 := buildID("testbuildexpose1", "80 2375")
	id2 := buildID("testbuildexpose2", "2375 80")
	if id1 != id2 {
		c.Errorf("EXPOSE should invalidate the cache only when ports actually changed")
	}
}

func (s *DockerSuite) TestBuildExposeUpperCaseProto(c *check.C) {
	testRequires(c, DaemonIsLinux) // Expose not implemented on Windows
	name := "testbuildexposeuppercaseproto"
	expected := "map[5678/udp:{}]"
	buildImageSuccessfully(c, name, withDockerfile(`FROM scratch
        EXPOSE 5678/UDP`))
	res := inspectField(c, name, "Config.ExposedPorts")
	if res != expected {
		c.Fatalf("Exposed ports %s, expected %s", res, expected)
	}
}

func (s *DockerSuite) TestBuildEmptyEntrypointInheritance(c *check.C) {
	name := "testbuildentrypointinheritance"
	name2 := "testbuildentrypointinheritance2"

	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
        ENTRYPOINT ["/bin/echo"]`))
	res := inspectField(c, name, "Config.Entrypoint")

	expected := "[/bin/echo]"
	if res != expected {
		c.Fatalf("Entrypoint %s, expected %s", res, expected)
	}

	buildImageSuccessfully(c, name2, withDockerfile(fmt.Sprintf(`FROM %s
        ENTRYPOINT []`, name)))
	res = inspectField(c, name2, "Config.Entrypoint")

	expected = "[]"
	if res != expected {
		c.Fatalf("Entrypoint %s, expected %s", res, expected)
	}
}

func (s *DockerSuite) TestBuildEmptyEntrypoint(c *check.C) {
	name := "testbuildentrypoint"
	expected := "[]"

	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
        ENTRYPOINT []`))

	res := inspectField(c, name, "Config.Entrypoint")
	if res != expected {
		c.Fatalf("Entrypoint %s, expected %s", res, expected)
	}

}

func (s *DockerSuite) TestBuildEntrypoint(c *check.C) {
	name := "testbuildentrypoint"

	expected := "[/bin/echo]"
	buildImageSuccessfully(c, name, withDockerfile(`FROM `+minimalBaseImage()+`
        ENTRYPOINT ["/bin/echo"]`))

	res := inspectField(c, name, "Config.Entrypoint")
	if res != expected {
		c.Fatalf("Entrypoint %s, expected %s", res, expected)
	}

}

// #6445 ensure ONBUILD triggers aren't committed to grandchildren
func (s *DockerSuite) TestBuildOnBuildLimitedInheritence(c *check.C) {
	buildImageSuccessfully(c, "testonbuildtrigger1", withDockerfile(`
		FROM busybox
		RUN echo "GRANDPARENT"
		ONBUILD RUN echo "ONBUILD PARENT"
		`))
	// ONBUILD should be run in second build.
	buildImage("testonbuildtrigger2", withDockerfile("FROM testonbuildtrigger1")).Assert(c, icmd.Expected{
		Out: "ONBUILD PARENT",
	})
	// ONBUILD should *not* be run in third build.
	result := buildImage("testonbuildtrigger3", withDockerfile("FROM testonbuildtrigger2"))
	result.Assert(c, icmd.Success)
	if strings.Contains(result.Combined(), "ONBUILD PARENT") {
		c.Fatalf("ONBUILD instruction ran in grandchild of ONBUILD parent")
	}
}

func (s *DockerSuite) TestBuildSameDockerfileWithAndWithoutCache(c *check.C) {
	testRequires(c, DaemonIsLinux) // Expose not implemented on Windows
	name := "testbuildwithcache"
	dockerfile := `FROM scratch
		MAINTAINER dockerio
		EXPOSE 5432
        ENTRYPOINT ["/bin/echo"]`
	buildImageSuccessfully(c, name, withDockerfile(dockerfile))
	id1 := getIDByName(c, name)
	buildImageSuccessfully(c, name, withDockerfile(dockerfile))
	id2 := getIDByName(c, name)
	buildImageSuccessfully(c, name, withoutCache, withDockerfile(dockerfile))
	id3 := getIDByName(c, name)
	if id1 != id2 {
		c.Fatal("The cache should have been used but hasn't.")
	}
	if id1 == id3 {
		c.Fatal("The cache should have been invalided but hasn't.")
	}
}

// Make sure that ADD/COPY still populate the cache even if they don't use it
func (s *DockerSuite) TestBuildConditionalCache(c *check.C) {
	name := "testbuildconditionalcache"

	dockerfile := `
		FROM busybox
        ADD foo /tmp/`
	ctx := fakeContext(c, dockerfile, map[string]string{
		"foo": "hello",
	})
	defer ctx.Close()

	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id1 := getIDByName(c, name)

	if err := ctx.Add("foo", "bye"); err != nil {
		c.Fatalf("Error modifying foo: %s", err)
	}

	// Updating a file should invalidate the cache
	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id2 := getIDByName(c, name)
	if id2 == id1 {
		c.Fatal("Should not have used the cache")
	}

	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id3 := getIDByName(c, name)
	if id3 != id2 {
		c.Fatal("Should have used the cache")
	}
}

// FIXME(vdemeester) this really seems to test the same thing as before
func (s *DockerSuite) TestBuildAddMultipleLocalFileWithAndWithoutCache(c *check.C) {
	name := "testbuildaddmultiplelocalfilewithcache"
	dockerfile := `
		FROM busybox
        MAINTAINER dockerio
        ADD foo Dockerfile /usr/lib/bla/
		RUN sh -c "[ $(cat /usr/lib/bla/foo) = "hello" ]"`
	ctx := fakeContext(c, dockerfile, map[string]string{
		"foo": "hello",
	})
	defer ctx.Close()
	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id1 := getIDByName(c, name)
	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id2 := getIDByName(c, name)
	buildImageSuccessfully(c, name, withoutCache, withExternalBuildContext(ctx))
	id3 := getIDByName(c, name)
	if id1 != id2 {
		c.Fatal("The cache should have been used but hasn't.")
	}
	if id1 == id3 {
		c.Fatal("The cache should have been invalided but hasn't.")
	}
}

func (s *DockerSuite) TestBuildCopyDirButNotFile(c *check.C) {
	name := "testbuildcopydirbutnotfile"
	name2 := "testbuildcopydirbutnotfile2"

	dockerfile := `
        FROM ` + minimalBaseImage() + `
        COPY dir /tmp/`
	ctx := fakeContext(c, dockerfile, map[string]string{
		"dir/foo": "hello",
	})
	defer ctx.Close()
	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id1 := getIDByName(c, name)
	// Check that adding file with similar name doesn't mess with cache
	if err := ctx.Add("dir_file", "hello2"); err != nil {
		c.Fatal(err)
	}
	buildImageSuccessfully(c, name2, withExternalBuildContext(ctx))
	id2 := getIDByName(c, name2)
	if id1 != id2 {
		c.Fatal("The cache should have been used but wasn't")
	}
}

func (s *DockerSuite) TestBuildAddCurrentDirWithCache(c *check.C) {
	name := "testbuildaddcurrentdirwithcache"
	name2 := name + "2"
	name3 := name + "3"
	name4 := name + "4"
	dockerfile := `
        FROM ` + minimalBaseImage() + `
        MAINTAINER dockerio
        ADD . /usr/lib/bla`
	ctx := fakeContext(c, dockerfile, map[string]string{
		"foo": "hello",
	})
	defer ctx.Close()
	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id1 := getIDByName(c, name)
	// Check that adding file invalidate cache of "ADD ."
	if err := ctx.Add("bar", "hello2"); err != nil {
		c.Fatal(err)
	}
	buildImageSuccessfully(c, name2, withExternalBuildContext(ctx))
	id2 := getIDByName(c, name2)
	if id1 == id2 {
		c.Fatal("The cache should have been invalided but hasn't.")
	}
	// Check that changing file invalidate cache of "ADD ."
	if err := ctx.Add("foo", "hello1"); err != nil {
		c.Fatal(err)
	}
	buildImageSuccessfully(c, name3, withExternalBuildContext(ctx))
	id3 := getIDByName(c, name3)
	if id2 == id3 {
		c.Fatal("The cache should have been invalided but hasn't.")
	}
	// Check that changing file to same content with different mtime does not
	// invalidate cache of "ADD ."
	time.Sleep(1 * time.Second) // wait second because of mtime precision
	if err := ctx.Add("foo", "hello1"); err != nil {
		c.Fatal(err)
	}
	buildImageSuccessfully(c, name4, withExternalBuildContext(ctx))
	id4 := getIDByName(c, name4)
	if id3 != id4 {
		c.Fatal("The cache should have been used but hasn't.")
	}
}

// FIXME(vdemeester) this really seems to test the same thing as before (TestBuildAddMultipleLocalFileWithAndWithoutCache)
func (s *DockerSuite) TestBuildAddCurrentDirWithoutCache(c *check.C) {
	name := "testbuildaddcurrentdirwithoutcache"
	dockerfile := `
        FROM ` + minimalBaseImage() + `
        MAINTAINER dockerio
        ADD . /usr/lib/bla`
	ctx := fakeContext(c, dockerfile, map[string]string{
		"foo": "hello",
	})
	defer ctx.Close()
	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id1 := getIDByName(c, name)
	buildImageSuccessfully(c, name, withoutCache, withExternalBuildContext(ctx))
	id2 := getIDByName(c, name)
	if id1 == id2 {
		c.Fatal("The cache should have been invalided but hasn't.")
	}
}

func (s *DockerSuite) TestBuildAddRemoteFileWithAndWithoutCache(c *check.C) {
	name := "testbuildaddremotefilewithcache"
	server := fakeStorage(c, map[string]string{
		"baz": "hello",
	})
	defer server.Close()

	dockerfile := fmt.Sprintf(`FROM `+minimalBaseImage()+`
        MAINTAINER dockerio
        ADD %s/baz /usr/lib/baz/quux`, server.URL())
	buildImageSuccessfully(c, name, withDockerfile(dockerfile))
	id1 := getIDByName(c, name)
	buildImageSuccessfully(c, name, withDockerfile(dockerfile))
	id2 := getIDByName(c, name)
	buildImageSuccessfully(c, name, withoutCache, withDockerfile(dockerfile))
	id3 := getIDByName(c, name)

	if id1 != id2 {
		c.Fatal("The cache should have been used but hasn't.")
	}
	if id1 == id3 {
		c.Fatal("The cache should have been invalided but hasn't.")
	}
}

func (s *DockerSuite) TestBuildAddRemoteFileMTime(c *check.C) {
	name := "testbuildaddremotefilemtime"
	name2 := name + "2"
	name3 := name + "3"

	files := map[string]string{"baz": "hello"}
	server := fakeStorage(c, files)
	defer server.Close()

	ctx := fakeContext(c, fmt.Sprintf(`FROM `+minimalBaseImage()+`
        MAINTAINER dockerio
        ADD %s/baz /usr/lib/baz/quux`, server.URL()), nil)
	defer ctx.Close()

	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id1 := getIDByName(c, name)
	buildImageSuccessfully(c, name2, withExternalBuildContext(ctx))
	id2 := getIDByName(c, name2)
	if id1 != id2 {
		c.Fatal("The cache should have been used but wasn't - #1")
	}

	// Now create a different server with same contents (causes different mtime)
	// The cache should still be used

	// allow some time for clock to pass as mtime precision is only 1s
	time.Sleep(2 * time.Second)

	server2 := fakeStorage(c, files)
	defer server2.Close()

	ctx2 := fakeContext(c, fmt.Sprintf(`FROM `+minimalBaseImage()+`
        MAINTAINER dockerio
        ADD %s/baz /usr/lib/baz/quux`, server2.URL()), nil)
	defer ctx2.Close()
	buildImageSuccessfully(c, name3, withExternalBuildContext(ctx2))
	id3 := getIDByName(c, name3)
	if id1 != id3 {
		c.Fatal("The cache should have been used but wasn't")
	}
}

// FIXME(vdemeester) this really seems to test the same thing as before (combined)
func (s *DockerSuite) TestBuildAddLocalAndRemoteFilesWithAndWithoutCache(c *check.C) {
	name := "testbuildaddlocalandremotefilewithcache"
	server := fakeStorage(c, map[string]string{
		"baz": "hello",
	})
	defer server.Close()

	ctx := fakeContext(c, fmt.Sprintf(`FROM `+minimalBaseImage()+`
        MAINTAINER dockerio
        ADD foo /usr/lib/bla/bar
        ADD %s/baz /usr/lib/baz/quux`, server.URL()),
		map[string]string{
			"foo": "hello world",
		})
	defer ctx.Close()
	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id1 := getIDByName(c, name)
	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id2 := getIDByName(c, name)
	buildImageSuccessfully(c, name, withoutCache, withExternalBuildContext(ctx))
	id3 := getIDByName(c, name)
	if id1 != id2 {
		c.Fatal("The cache should have been used but hasn't.")
	}
	if id1 == id3 {
		c.Fatal("The cache should have been invalidated but hasn't.")
	}
}

func testContextTar(c *check.C, compression archive.Compression) {
	ctx := fakeContext(c,
		`FROM busybox
ADD foo /foo
CMD ["cat", "/foo"]`,
		map[string]string{
			"foo": "bar",
		},
	)
	defer ctx.Close()
	context, err := archive.Tar(ctx.Dir, compression)
	if err != nil {
		c.Fatalf("failed to build context tar: %v", err)
	}
	name := "contexttar"

	icmd.RunCmd(icmd.Cmd{
		Command: []string{dockerBinary, "build", "-t", name, "-"},
		Stdin:   context,
	}).Assert(c, icmd.Success)
}

func (s *DockerSuite) TestBuildContextTarGzip(c *check.C) {
	testContextTar(c, archive.Gzip)
}

func (s *DockerSuite) TestBuildContextTarNoCompression(c *check.C) {
	testContextTar(c, archive.Uncompressed)
}

func (s *DockerSuite) TestBuildNoContext(c *check.C) {
	name := "nocontext"
	icmd.RunCmd(icmd.Cmd{
		Command: []string{dockerBinary, "build", "-t", name, "-"},
		Stdin: strings.NewReader(
			`FROM busybox
			CMD ["echo", "ok"]`),
	}).Assert(c, icmd.Success)

	if out, _ := dockerCmd(c, "run", "--rm", "nocontext"); out != "ok\n" {
		c.Fatalf("run produced invalid output: %q, expected %q", out, "ok")
	}
}

func (s *DockerSuite) TestBuildWithVolumeOwnership(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildimg"

	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox:latest
        RUN mkdir /test && chown daemon:daemon /test && chmod 0600 /test
        VOLUME /test`))

	out, _ := dockerCmd(c, "run", "--rm", "testbuildimg", "ls", "-la", "/test")
	if expected := "drw-------"; !strings.Contains(out, expected) {
		c.Fatalf("expected %s received %s", expected, out)
	}
	if expected := "daemon   daemon"; !strings.Contains(out, expected) {
		c.Fatalf("expected %s received %s", expected, out)
	}

}

// testing #1405 - config.Cmd does not get cleaned up if
// utilizing cache
func (s *DockerSuite) TestBuildEntrypointRunCleanup(c *check.C) {
	name := "testbuildcmdcleanup"
	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
        RUN echo "hello"`))

	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", `FROM busybox
        RUN echo "hello"
        ADD foo /foo
        ENTRYPOINT ["/bin/echo"]`),
		withFile("foo", "hello")))

	res := inspectField(c, name, "Config.Cmd")
	// Cmd must be cleaned up
	if res != "[]" {
		c.Fatalf("Cmd %s, expected nil", res)
	}
}

func (s *DockerSuite) TestBuildAddFileNotFound(c *check.C) {
	name := "testbuildaddnotfound"
	expected := "foo: no such file or directory"

	if testEnv.DaemonPlatform() == "windows" {
		expected = "foo: The system cannot find the file specified"
	}

	buildImage(name, withBuildContext(c,
		withFile("Dockerfile", `FROM `+minimalBaseImage()+`
        ADD foo /usr/local/bar`),
		withFile("bar", "hello"))).Assert(c, icmd.Expected{
		ExitCode: 1,
		Err:      expected,
	})
}

func (s *DockerSuite) TestBuildInheritance(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildinheritance"

	buildImageSuccessfully(c, name, withDockerfile(`FROM scratch
		EXPOSE 2375`))
	ports1 := inspectField(c, name, "Config.ExposedPorts")

	buildImageSuccessfully(c, name, withDockerfile(fmt.Sprintf(`FROM %s
		ENTRYPOINT ["/bin/echo"]`, name)))

	res := inspectField(c, name, "Config.Entrypoint")
	if expected := "[/bin/echo]"; res != expected {
		c.Fatalf("Entrypoint %s, expected %s", res, expected)
	}
	ports2 := inspectField(c, name, "Config.ExposedPorts")
	if ports1 != ports2 {
		c.Fatalf("Ports must be same: %s != %s", ports1, ports2)
	}
}

func (s *DockerSuite) TestBuildFails(c *check.C) {
	name := "testbuildfails"
	buildImage(name, withDockerfile(`FROM busybox
		RUN sh -c "exit 23"`)).Assert(c, icmd.Expected{
		ExitCode: 23,
		Err:      "returned a non-zero code: 23",
	})
}

func (s *DockerSuite) TestBuildOnBuild(c *check.C) {
	name := "testbuildonbuild"
	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
		ONBUILD RUN touch foobar`))
	buildImageSuccessfully(c, name, withDockerfile(fmt.Sprintf(`FROM %s
		RUN [ -f foobar ]`, name)))
}

// gh #2446
func (s *DockerSuite) TestBuildAddToSymlinkDest(c *check.C) {
	makeLink := `ln -s /foo /bar`
	if testEnv.DaemonPlatform() == "windows" {
		makeLink = `mklink /D C:\bar C:\foo`
	}
	name := "testbuildaddtosymlinkdest"
	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", `
		FROM busybox
		RUN sh -c "mkdir /foo"
		RUN `+makeLink+`
		ADD foo /bar/
		RUN sh -c "[ -f /bar/foo ]"
		RUN sh -c "[ -f /foo/foo ]"`),
		withFile("foo", "hello"),
	))
}

func (s *DockerSuite) TestBuildEscapeWhitespace(c *check.C) {
	name := "testbuildescapewhitespace"

	buildImageSuccessfully(c, name, withDockerfile(`
  # ESCAPE=\
  FROM busybox
  MAINTAINER "Docker \
IO <io@\
docker.com>"
  `))

	res := inspectField(c, name, "Author")
	if res != "\"Docker IO <io@docker.com>\"" {
		c.Fatalf("Parsed string did not match the escaped string. Got: %q", res)
	}

}

func (s *DockerSuite) TestBuildVerifyIntString(c *check.C) {
	// Verify that strings that look like ints are still passed as strings
	name := "testbuildstringing"

	buildImageSuccessfully(c, name, withDockerfile(`
	FROM busybox
	MAINTAINER 123`))

	out, _ := dockerCmd(c, "inspect", name)
	if !strings.Contains(out, "\"123\"") {
		c.Fatalf("Output does not contain the int as a string:\n%s", out)
	}

}

func (s *DockerSuite) TestBuildDockerignore(c *check.C) {
	name := "testbuilddockerignore"
	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", `
		FROM busybox
		 ADD . /bla
		RUN sh -c "[[ -f /bla/src/x.go ]]"
		RUN sh -c "[[ -f /bla/Makefile ]]"
		RUN sh -c "[[ ! -e /bla/src/_vendor ]]"
		RUN sh -c "[[ ! -e /bla/.gitignore ]]"
		RUN sh -c "[[ ! -e /bla/README.md ]]"
		RUN sh -c "[[ ! -e /bla/dir/foo ]]"
		RUN sh -c "[[ ! -e /bla/foo ]]"
		RUN sh -c "[[ ! -e /bla/.git ]]"
		RUN sh -c "[[ ! -e v.cc ]]"
		RUN sh -c "[[ ! -e src/v.cc ]]"
		RUN sh -c "[[ ! -e src/_vendor/v.cc ]]"`),
		withFile("Makefile", "all:"),
		withFile(".git/HEAD", "ref: foo"),
		withFile("src/x.go", "package main"),
		withFile("src/_vendor/v.go", "package main"),
		withFile("src/_vendor/v.cc", "package main"),
		withFile("src/v.cc", "package main"),
		withFile("v.cc", "package main"),
		withFile("dir/foo", ""),
		withFile(".gitignore", ""),
		withFile("README.md", "readme"),
		withFile(".dockerignore", `
.git
pkg
.gitignore
src/_vendor
*.md
**/*.cc
dir`),
	))
}

func (s *DockerSuite) TestBuildDockerignoreCleanPaths(c *check.C) {
	name := "testbuilddockerignorecleanpaths"
	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", `
        FROM busybox
        ADD . /tmp/
        RUN sh -c "(! ls /tmp/foo) && (! ls /tmp/foo2) && (! ls /tmp/dir1/foo)"`),
		withFile("foo", "foo"),
		withFile("foo2", "foo2"),
		withFile("dir1/foo", "foo in dir1"),
		withFile(".dockerignore", "./foo\ndir1//foo\n./dir1/../foo2"),
	))
}

func (s *DockerSuite) TestBuildDockerignoreExceptions(c *check.C) {
	name := "testbuilddockerignoreexceptions"
	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", `
		FROM busybox
		ADD . /bla
		RUN sh -c "[[ -f /bla/src/x.go ]]"
		RUN sh -c "[[ -f /bla/Makefile ]]"
		RUN sh -c "[[ ! -e /bla/src/_vendor ]]"
		RUN sh -c "[[ ! -e /bla/.gitignore ]]"
		RUN sh -c "[[ ! -e /bla/README.md ]]"
		RUN sh -c "[[  -e /bla/dir/dir/foo ]]"
		RUN sh -c "[[ ! -e /bla/dir/foo1 ]]"
		RUN sh -c "[[ -f /bla/dir/e ]]"
		RUN sh -c "[[ -f /bla/dir/e-dir/foo ]]"
		RUN sh -c "[[ ! -e /bla/foo ]]"
		RUN sh -c "[[ ! -e /bla/.git ]]"
		RUN sh -c "[[ -e /bla/dir/a.cc ]]"`),
		withFile("Makefile", "all:"),
		withFile(".git/HEAD", "ref: foo"),
		withFile("src/x.go", "package main"),
		withFile("src/_vendor/v.go", "package main"),
		withFile("dir/foo", ""),
		withFile("dir/foo1", ""),
		withFile("dir/dir/f1", ""),
		withFile("dir/dir/foo", ""),
		withFile("dir/e", ""),
		withFile("dir/e-dir/foo", ""),
		withFile(".gitignore", ""),
		withFile("README.md", "readme"),
		withFile("dir/a.cc", "hello"),
		withFile(".dockerignore", `
.git
pkg
.gitignore
src/_vendor
*.md
dir
!dir/e*
!dir/dir/foo
**/*.cc
!**/*.cc`),
	))
}

func (s *DockerSuite) TestBuildDockerignoringDockerfile(c *check.C) {
	name := "testbuilddockerignoredockerfile"
	dockerfile := `
		FROM busybox
		ADD . /tmp/
		RUN sh -c "! ls /tmp/Dockerfile"
		RUN ls /tmp/.dockerignore`
	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", dockerfile),
		withFile(".dockerignore", "Dockerfile\n"),
	))
	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", dockerfile),
		withFile(".dockerignore", "./Dockerfile\n"),
	))
}

func (s *DockerSuite) TestBuildDockerignoringRenamedDockerfile(c *check.C) {
	name := "testbuilddockerignoredockerfile"
	dockerfile := `
		FROM busybox
		ADD . /tmp/
		RUN ls /tmp/Dockerfile
		RUN sh -c "! ls /tmp/MyDockerfile"
		RUN ls /tmp/.dockerignore`
	buildImageSuccessfully(c, name, withBuildFlags("-f", "MyDockerfile"), withBuildContext(c,
		withFile("Dockerfile", "Should not use me"),
		withFile("MyDockerfile", dockerfile),
		withFile(".dockerignore", "MyDockerfile\n"),
	))
	buildImageSuccessfully(c, name, withBuildFlags("-f", "MyDockerfile"), withBuildContext(c,
		withFile("Dockerfile", "Should not use me"),
		withFile("MyDockerfile", dockerfile),
		withFile(".dockerignore", "./MyDockerfile\n"),
	))
}

func (s *DockerSuite) TestBuildDockerignoringDockerignore(c *check.C) {
	name := "testbuilddockerignoredockerignore"
	dockerfile := `
		FROM busybox
		ADD . /tmp/
		RUN sh -c "! ls /tmp/.dockerignore"
		RUN ls /tmp/Dockerfile`
	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", dockerfile),
		withFile(".dockerignore", ".dockerignore\n"),
	))
}

func (s *DockerSuite) TestBuildDockerignoreTouchDockerfile(c *check.C) {
	name := "testbuilddockerignoretouchdockerfile"
	dockerfile := `
        FROM busybox
		ADD . /tmp/`
	ctx := fakeContext(c, dockerfile, map[string]string{
		"Dockerfile":    dockerfile,
		".dockerignore": "Dockerfile\n",
	})
	defer ctx.Close()

	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id1 := getIDByName(c, name)

	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id2 := getIDByName(c, name)
	if id1 != id2 {
		c.Fatalf("Didn't use the cache - 1")
	}

	// Now make sure touching Dockerfile doesn't invalidate the cache
	if err := ctx.Add("Dockerfile", dockerfile+"\n# hi"); err != nil {
		c.Fatalf("Didn't add Dockerfile: %s", err)
	}
	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id2 = getIDByName(c, name)
	if id1 != id2 {
		c.Fatalf("Didn't use the cache - 2")
	}

	// One more time but just 'touch' it instead of changing the content
	if err := ctx.Add("Dockerfile", dockerfile+"\n# hi"); err != nil {
		c.Fatalf("Didn't add Dockerfile: %s", err)
	}
	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
	id2 = getIDByName(c, name)
	if id1 != id2 {
		c.Fatalf("Didn't use the cache - 3")
	}
}

func (s *DockerSuite) TestBuildDockerignoringWholeDir(c *check.C) {
	name := "testbuilddockerignorewholedir"

	dockerfile := `
		FROM busybox
		COPY . /
		RUN sh -c "[[ ! -e /.gitignore ]]"
		RUN sh -c "[[ ! -e /Makefile ]]"`

	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", dockerfile),
		withFile(".dockerignore", "*\n"),
		withFile("Makefile", "all:"),
		withFile(".gitignore", ""),
	))
}

func (s *DockerSuite) TestBuildDockerignoringOnlyDotfiles(c *check.C) {
	name := "testbuilddockerignorewholedir"

	dockerfile := `
		FROM busybox
		COPY . /
		RUN sh -c "[[ ! -e /.gitignore ]]"
		RUN sh -c "[[ -f /Makefile ]]"`

	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", dockerfile),
		withFile(".dockerignore", ".*"),
		withFile("Makefile", "all:"),
		withFile(".gitignore", ""),
	))
}

func (s *DockerSuite) TestBuildDockerignoringBadExclusion(c *check.C) {
	name := "testbuilddockerignorebadexclusion"
	buildImage(name, withBuildContext(c,
		withFile("Dockerfile", `
		FROM busybox
		COPY . /
		RUN sh -c "[[ ! -e /.gitignore ]]"
		RUN sh -c "[[ -f /Makefile ]]"`),
		withFile("Makefile", "all:"),
		withFile(".gitignore", ""),
		withFile(".dockerignore", "!\n"),
	)).Assert(c, icmd.Expected{
		ExitCode: 1,
		Err:      "Error checking context: 'illegal exclusion pattern: \"!\"",
	})
}

func (s *DockerSuite) TestBuildDockerignoringWildTopDir(c *check.C) {
	dockerfile := `
		FROM busybox
		COPY . /
		RUN sh -c "[[ ! -e /.dockerignore ]]"
		RUN sh -c "[[ ! -e /Dockerfile ]]"
		RUN sh -c "[[ ! -e /file1 ]]"
		RUN sh -c "[[ ! -e /dir ]]"`

	// All of these should result in ignoring all files
	for _, variant := range []string{"**", "**/", "**/**", "*"} {
		buildImageSuccessfully(c, "noname", withBuildContext(c,
			withFile("Dockerfile", dockerfile),
			withFile("file1", ""),
			withFile("dir/file1", ""),
			withFile(".dockerignore", variant),
		))

		dockerCmd(c, "rmi", "noname")
	}
}

func (s *DockerSuite) TestBuildDockerignoringWildDirs(c *check.C) {
	dockerfile := `
        FROM busybox
		COPY . /
		#RUN sh -c "[[ -e /.dockerignore ]]"
		RUN sh -c "[[ -e /Dockerfile ]]           && \
		           [[ ! -e /file0 ]]              && \
		           [[ ! -e /dir1/file0 ]]         && \
		           [[ ! -e /dir2/file0 ]]         && \
		           [[ ! -e /file1 ]]              && \
		           [[ ! -e /dir1/file1 ]]         && \
		           [[ ! -e /dir1/dir2/file1 ]]    && \
		           [[ ! -e /dir1/file2 ]]         && \
		           [[   -e /dir1/dir2/file2 ]]    && \
		           [[ ! -e /dir1/dir2/file4 ]]    && \
		           [[ ! -e /dir1/dir2/file5 ]]    && \
		           [[ ! -e /dir1/dir2/file6 ]]    && \
		           [[ ! -e /dir1/dir3/file7 ]]    && \
		           [[ ! -e /dir1/dir3/file8 ]]    && \
		           [[   -e /dir1/dir3 ]]          && \
		           [[   -e /dir1/dir4 ]]          && \
		           [[ ! -e 'dir1/dir5/fileAA' ]]  && \
		           [[   -e 'dir1/dir5/fileAB' ]]  && \
		           [[   -e 'dir1/dir5/fileB' ]]"   # "." in pattern means nothing

		RUN echo all done!`

	dockerignore := `
**/file0
**/*file1
**/dir1/file2
dir1/**/file4
**/dir2/file5
**/dir1/dir2/file6
dir1/dir3/**
**/dir4/**
**/file?A
**/file\?B
**/dir5/file.
`

	buildImageSuccessfully(c, "noname", withBuildContext(c,
		withFile("Dockerfile", dockerfile),
		withFile(".dockerignore", dockerignore),
		withFile("dir1/file0", ""),
		withFile("dir1/dir2/file0", ""),
		withFile("file1", ""),
		withFile("dir1/file1", ""),
		withFile("dir1/dir2/file1", ""),
		withFile("dir1/file2", ""),
		withFile("dir1/dir2/file2", ""), // remains
		withFile("dir1/dir2/file4", ""),
		withFile("dir1/dir2/file5", ""),
		withFile("dir1/dir2/file6", ""),
		withFile("dir1/dir3/file7", ""),
		withFile("dir1/dir3/file8", ""),
		withFile("dir1/dir4/file9", ""),
		withFile("dir1/dir5/fileAA", ""),
		withFile("dir1/dir5/fileAB", ""),
		withFile("dir1/dir5/fileB", ""),
	))
}

func (s *DockerSuite) TestBuildLineBreak(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildlinebreak"
	buildImageSuccessfully(c, name, withDockerfile(`FROM  busybox
RUN    sh -c 'echo root:testpass \
	> /tmp/passwd'
RUN    mkdir -p /var/run/sshd
RUN    sh -c "[ "$(cat /tmp/passwd)" = "root:testpass" ]"
RUN    sh -c "[ "$(ls -d /var/run/sshd)" = "/var/run/sshd" ]"`))
}

func (s *DockerSuite) TestBuildEOLInLine(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildeolinline"
	buildImageSuccessfully(c, name, withDockerfile(`FROM   busybox
RUN    sh -c 'echo root:testpass > /tmp/passwd'
RUN    echo "foo \n bar"; echo "baz"
RUN    mkdir -p /var/run/sshd
RUN    sh -c "[ "$(cat /tmp/passwd)" = "root:testpass" ]"
RUN    sh -c "[ "$(ls -d /var/run/sshd)" = "/var/run/sshd" ]"`))
}

func (s *DockerSuite) TestBuildCommentsShebangs(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildcomments"
	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
# This is an ordinary comment.
RUN { echo '#!/bin/sh'; echo 'echo hello world'; } > /hello.sh
RUN [ ! -x /hello.sh ]
# comment with line break \
RUN chmod +x /hello.sh
RUN [ -x /hello.sh ]
RUN [ "$(cat /hello.sh)" = $'#!/bin/sh\necho hello world' ]
RUN [ "$(/hello.sh)" = "hello world" ]`))
}

func (s *DockerSuite) TestBuildUsersAndGroups(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildusers"
	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox

# Make sure our defaults work
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)" = '0:0/root:root' ]

# TODO decide if "args.user = strconv.Itoa(syscall.Getuid())" is acceptable behavior for changeUser in sysvinit instead of "return nil" when "USER" isn't specified (so that we get the proper group list even if that is the empty list, even in the default case of not supplying an explicit USER to run as, which implies USER 0)
USER root
RUN [ "$(id -G):$(id -Gn)" = '0 10:root wheel' ]

# Setup dockerio user and group
RUN echo 'dockerio:x:1001:1001::/bin:/bin/false' >> /etc/passwd && \
	echo 'dockerio:x:1001:' >> /etc/group

# Make sure we can switch to our user and all the information is exactly as we expect it to be
USER dockerio
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1001/dockerio:dockerio/1001:dockerio' ]

# Switch back to root and double check that worked exactly as we might expect it to
USER root
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '0:0/root:root/0 10:root wheel' ] && \
        # Add a "supplementary" group for our dockerio user
	echo 'supplementary:x:1002:dockerio' >> /etc/group

# ... and then go verify that we get it like we expect
USER dockerio
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1001/dockerio:dockerio/1001 1002:dockerio supplementary' ]
USER 1001
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1001/dockerio:dockerio/1001 1002:dockerio supplementary' ]

# super test the new "user:group" syntax
USER dockerio:dockerio
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1001/dockerio:dockerio/1001:dockerio' ]
USER 1001:dockerio
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1001/dockerio:dockerio/1001:dockerio' ]
USER dockerio:1001
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1001/dockerio:dockerio/1001:dockerio' ]
USER 1001:1001
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1001/dockerio:dockerio/1001:dockerio' ]
USER dockerio:supplementary
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1002/dockerio:supplementary/1002:supplementary' ]
USER dockerio:1002
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1002/dockerio:supplementary/1002:supplementary' ]
USER 1001:supplementary
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1002/dockerio:supplementary/1002:supplementary' ]
USER 1001:1002
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1002/dockerio:supplementary/1002:supplementary' ]

# make sure unknown uid/gid still works properly
USER 1042:1043
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1042:1043/1042:1043/1043:1043' ]`))
}

// FIXME(vdemeester) rename this test (and probably "merge" it with the one below TestBuildEnvUsage2)
func (s *DockerSuite) TestBuildEnvUsage(c *check.C) {
	// /docker/world/hello is not owned by the correct user
	testRequires(c, NotUserNamespace)
	testRequires(c, DaemonIsLinux)
	name := "testbuildenvusage"
	dockerfile := `FROM busybox
ENV    HOME /root
ENV    PATH $HOME/bin:$PATH
ENV    PATH /tmp:$PATH
RUN    [ "$PATH" = "/tmp:$HOME/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" ]
ENV    FOO /foo/baz
ENV    BAR /bar
ENV    BAZ $BAR
ENV    FOOPATH $PATH:$FOO
RUN    [ "$BAR" = "$BAZ" ]
RUN    [ "$FOOPATH" = "$PATH:/foo/baz" ]
ENV    FROM hello/docker/world
ENV    TO /docker/world/hello
ADD    $FROM $TO
RUN    [ "$(cat $TO)" = "hello" ]
ENV    abc=def
ENV    ghi=$abc
RUN    [ "$ghi" = "def" ]
`
	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", dockerfile),
		withFile("hello/docker/world", "hello"),
	))
}

// FIXME(vdemeester) rename this test (and probably "merge" it with the one above TestBuildEnvUsage)
func (s *DockerSuite) TestBuildEnvUsage2(c *check.C) {
	// /docker/world/hello is not owned by the correct user
	testRequires(c, NotUserNamespace)
	testRequires(c, DaemonIsLinux)
	name := "testbuildenvusage2"
	dockerfile := `FROM busybox
ENV    abc=def def="hello world"
RUN    [ "$abc,$def" = "def,hello world" ]
ENV    def=hello\ world v1=abc v2="hi there" v3='boogie nights' v4="with'quotes too"
RUN    [ "$def,$v1,$v2,$v3,$v4" = "hello world,abc,hi there,boogie nights,with'quotes too" ]
ENV    abc=zzz FROM=hello/docker/world
ENV    abc=zzz TO=/docker/world/hello
ADD    $FROM $TO
RUN    [ "$abc,$(cat $TO)" = "zzz,hello" ]
ENV    abc 'yyy'
RUN    [ $abc = 'yyy' ]
ENV    abc=
RUN    [ "$abc" = "" ]

# use grep to make sure if the builder substitutes \$foo by mistake
# we don't get a false positive
ENV    abc=\$foo
RUN    [ "$abc" = "\$foo" ] && (echo "$abc" | grep foo)
ENV    abc \$foo
RUN    [ "$abc" = "\$foo" ] && (echo "$abc" | grep foo)

ENV    abc=\'foo\' abc2=\"foo\"
RUN    [ "$abc,$abc2" = "'foo',\"foo\"" ]
ENV    abc "foo"
RUN    [ "$abc" = "foo" ]
ENV    abc 'foo'
RUN    [ "$abc" = 'foo' ]
ENV    abc \'foo\'
RUN    [ "$abc" = "'foo'" ]
ENV    abc \"foo\"
RUN    [ "$abc" = '"foo"' ]

ENV    abc=ABC
RUN    [ "$abc" = "ABC" ]
ENV    def1=${abc:-DEF} def2=${ccc:-DEF}
ENV    def3=${ccc:-${def2}xx} def4=${abc:+ALT} def5=${def2:+${abc}:} def6=${ccc:-\$abc:} def7=${ccc:-\${abc}:}
RUN    [ "$def1,$def2,$def3,$def4,$def5,$def6,$def7" = 'ABC,DEF,DEFxx,ALT,ABC:,$abc:,${abc:}' ]
ENV    mypath=${mypath:+$mypath:}/home
ENV    mypath=${mypath:+$mypath:}/away
RUN    [ "$mypath" = '/home:/away' ]

ENV    e1=bar
ENV    e2=$e1 e3=$e11 e4=\$e1 e5=\$e11
RUN    [ "$e0,$e1,$e2,$e3,$e4,$e5" = ',bar,bar,,$e1,$e11' ]

ENV    ee1 bar
ENV    ee2 $ee1
ENV    ee3 $ee11
ENV    ee4 \$ee1
ENV    ee5 \$ee11
RUN    [ "$ee1,$ee2,$ee3,$ee4,$ee5" = 'bar,bar,,$ee1,$ee11' ]

ENV    eee1="foo" eee2='foo'
ENV    eee3 "foo"
ENV    eee4 'foo'
RUN    [ "$eee1,$eee2,$eee3,$eee4" = 'foo,foo,foo,foo' ]

`
	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", dockerfile),
		withFile("hello/docker/world", "hello"),
	))
}

func (s *DockerSuite) TestBuildAddScript(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildaddscript"
	dockerfile := `
FROM busybox
ADD test /test
RUN ["chmod","+x","/test"]
RUN ["/test"]
RUN [ "$(cat /testfile)" = 'test!' ]`

	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", dockerfile),
		withFile("test", "#!/bin/sh\necho 'test!' > /testfile"),
	))
}

func (s *DockerSuite) TestBuildAddTar(c *check.C) {
	// /test/foo is not owned by the correct user
	testRequires(c, NotUserNamespace)
	name := "testbuildaddtar"

	ctx := func() *FakeContext {
		dockerfile := `
FROM busybox
ADD test.tar /
RUN cat /test/foo | grep Hi
ADD test.tar /test.tar
RUN cat /test.tar/test/foo | grep Hi
ADD test.tar /unlikely-to-exist
RUN cat /unlikely-to-exist/test/foo | grep Hi
ADD test.tar /unlikely-to-exist-trailing-slash/
RUN cat /unlikely-to-exist-trailing-slash/test/foo | grep Hi
RUN sh -c "mkdir /existing-directory" #sh -c is needed on Windows to use the correct mkdir
ADD test.tar /existing-directory
RUN cat /existing-directory/test/foo | grep Hi
ADD test.tar /existing-directory-trailing-slash/
RUN cat /existing-directory-trailing-slash/test/foo | grep Hi`
		tmpDir, err := ioutil.TempDir("", "fake-context")
		c.Assert(err, check.IsNil)
		testTar, err := os.Create(filepath.Join(tmpDir, "test.tar"))
		if err != nil {
			c.Fatalf("failed to create test.tar archive: %v", err)
		}
		defer testTar.Close()

		tw := tar.NewWriter(testTar)

		if err := tw.WriteHeader(&tar.Header{
			Name: "test/foo",
			Size: 2,
		}); err != nil {
			c.Fatalf("failed to write tar file header: %v", err)
		}
		if _, err := tw.Write([]byte("Hi")); err != nil {
			c.Fatalf("failed to write tar file content: %v", err)
		}
		if err := tw.Close(); err != nil {
			c.Fatalf("failed to close tar archive: %v", err)
		}

		if err := ioutil.WriteFile(filepath.Join(tmpDir, "Dockerfile"), []byte(dockerfile), 0644); err != nil {
			c.Fatalf("failed to open destination dockerfile: %v", err)
		}
		return fakeContextFromDir(tmpDir)
	}()
	defer ctx.Close()

	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
}

func (s *DockerSuite) TestBuildAddBrokenTar(c *check.C) {
	name := "testbuildaddbrokentar"

	ctx := func() *FakeContext {
		dockerfile := `
FROM busybox
ADD test.tar /`
		tmpDir, err := ioutil.TempDir("", "fake-context")
		c.Assert(err, check.IsNil)
		testTar, err := os.Create(filepath.Join(tmpDir, "test.tar"))
		if err != nil {
			c.Fatalf("failed to create test.tar archive: %v", err)
		}
		defer testTar.Close()

		tw := tar.NewWriter(testTar)

		if err := tw.WriteHeader(&tar.Header{
			Name: "test/foo",
			Size: 2,
		}); err != nil {
			c.Fatalf("failed to write tar file header: %v", err)
		}
		if _, err := tw.Write([]byte("Hi")); err != nil {
			c.Fatalf("failed to write tar file content: %v", err)
		}
		if err := tw.Close(); err != nil {
			c.Fatalf("failed to close tar archive: %v", err)
		}

		// Corrupt the tar by removing one byte off the end
		stat, err := testTar.Stat()
		if err != nil {
			c.Fatalf("failed to stat tar archive: %v", err)
		}
		if err := testTar.Truncate(stat.Size() - 1); err != nil {
			c.Fatalf("failed to truncate tar archive: %v", err)
		}

		if err := ioutil.WriteFile(filepath.Join(tmpDir, "Dockerfile"), []byte(dockerfile), 0644); err != nil {
			c.Fatalf("failed to open destination dockerfile: %v", err)
		}
		return fakeContextFromDir(tmpDir)
	}()
	defer ctx.Close()

	buildImage(name, withExternalBuildContext(ctx)).Assert(c, icmd.Expected{
		ExitCode: 1,
	})
}

func (s *DockerSuite) TestBuildAddNonTar(c *check.C) {
	name := "testbuildaddnontar"

	// Should not try to extract test.tar
	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", `
		FROM busybox
		ADD test.tar /
		RUN test -f /test.tar`),
		withFile("test.tar", "not_a_tar_file"),
	))
}

func (s *DockerSuite) TestBuildAddTarXz(c *check.C) {
	// /test/foo is not owned by the correct user
	testRequires(c, NotUserNamespace)
	testRequires(c, DaemonIsLinux)
	name := "testbuildaddtarxz"

	ctx := func() *FakeContext {
		dockerfile := `
			FROM busybox
			ADD test.tar.xz /
			RUN cat /test/foo | grep Hi`
		tmpDir, err := ioutil.TempDir("", "fake-context")
		c.Assert(err, check.IsNil)
		testTar, err := os.Create(filepath.Join(tmpDir, "test.tar"))
		if err != nil {
			c.Fatalf("failed to create test.tar archive: %v", err)
		}
		defer testTar.Close()

		tw := tar.NewWriter(testTar)

		if err := tw.WriteHeader(&tar.Header{
			Name: "test/foo",
			Size: 2,
		}); err != nil {
			c.Fatalf("failed to write tar file header: %v", err)
		}
		if _, err := tw.Write([]byte("Hi")); err != nil {
			c.Fatalf("failed to write tar file content: %v", err)
		}
		if err := tw.Close(); err != nil {
			c.Fatalf("failed to close tar archive: %v", err)
		}

		icmd.RunCmd(icmd.Cmd{
			Command: []string{"xz", "-k", "test.tar"},
			Dir:     tmpDir,
		}).Assert(c, icmd.Success)
		if err := ioutil.WriteFile(filepath.Join(tmpDir, "Dockerfile"), []byte(dockerfile), 0644); err != nil {
			c.Fatalf("failed to open destination dockerfile: %v", err)
		}
		return fakeContextFromDir(tmpDir)
	}()

	defer ctx.Close()

	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
}

func (s *DockerSuite) TestBuildAddTarXzGz(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildaddtarxzgz"

	ctx := func() *FakeContext {
		dockerfile := `
			FROM busybox
			ADD test.tar.xz.gz /
			RUN ls /test.tar.xz.gz`
		tmpDir, err := ioutil.TempDir("", "fake-context")
		c.Assert(err, check.IsNil)
		testTar, err := os.Create(filepath.Join(tmpDir, "test.tar"))
		if err != nil {
			c.Fatalf("failed to create test.tar archive: %v", err)
		}
		defer testTar.Close()

		tw := tar.NewWriter(testTar)

		if err := tw.WriteHeader(&tar.Header{
			Name: "test/foo",
			Size: 2,
		}); err != nil {
			c.Fatalf("failed to write tar file header: %v", err)
		}
		if _, err := tw.Write([]byte("Hi")); err != nil {
			c.Fatalf("failed to write tar file content: %v", err)
		}
		if err := tw.Close(); err != nil {
			c.Fatalf("failed to close tar archive: %v", err)
		}

		icmd.RunCmd(icmd.Cmd{
			Command: []string{"xz", "-k", "test.tar"},
			Dir:     tmpDir,
		}).Assert(c, icmd.Success)

		icmd.RunCmd(icmd.Cmd{
			Command: []string{"gzip", "test.tar.xz"},
			Dir:     tmpDir,
		})
		if err := ioutil.WriteFile(filepath.Join(tmpDir, "Dockerfile"), []byte(dockerfile), 0644); err != nil {
			c.Fatalf("failed to open destination dockerfile: %v", err)
		}
		return fakeContextFromDir(tmpDir)
	}()

	defer ctx.Close()

	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
}

func (s *DockerSuite) TestBuildFromGit(c *check.C) {
	name := "testbuildfromgit"
	git := newFakeGit(c, "repo", map[string]string{
		"Dockerfile": `FROM busybox
		ADD first /first
		RUN [ -f /first ]
		MAINTAINER docker`,
		"first": "test git data",
	}, true)
	defer git.Close()

	buildImageSuccessfully(c, name, withBuildContextPath(git.RepoURL))

	res := inspectField(c, name, "Author")
	if res != "docker" {
		c.Fatalf("Maintainer should be docker, got %s", res)
	}
}

func (s *DockerSuite) TestBuildFromGitWithContext(c *check.C) {
	name := "testbuildfromgit"
	git := newFakeGit(c, "repo", map[string]string{
		"docker/Dockerfile": `FROM busybox
					ADD first /first
					RUN [ -f /first ]
					MAINTAINER docker`,
		"docker/first": "test git data",
	}, true)
	defer git.Close()

	buildImageSuccessfully(c, name, withBuildContextPath(fmt.Sprintf("%s#master:docker", git.RepoURL)))

	res := inspectField(c, name, "Author")
	if res != "docker" {
		c.Fatalf("Maintainer should be docker, got %s", res)
	}
}

func (s *DockerSuite) TestBuildFromGitwithF(c *check.C) {
	name := "testbuildfromgitwithf"
	git := newFakeGit(c, "repo", map[string]string{
		"myApp/myDockerfile": `FROM busybox
					RUN echo hi from Dockerfile`,
	}, true)
	defer git.Close()

	buildImage(name, withBuildFlags("-f", "myApp/myDockerfile"), withBuildContextPath(git.RepoURL)).Assert(c, icmd.Expected{
		Out: "hi from Dockerfile",
	})
}

func (s *DockerSuite) TestBuildFromRemoteTarball(c *check.C) {
	name := "testbuildfromremotetarball"

	buffer := new(bytes.Buffer)
	tw := tar.NewWriter(buffer)
	defer tw.Close()

	dockerfile := []byte(`FROM busybox
					MAINTAINER docker`)
	if err := tw.WriteHeader(&tar.Header{
		Name: "Dockerfile",
		Size: int64(len(dockerfile)),
	}); err != nil {
		c.Fatalf("failed to write tar file header: %v", err)
	}
	if _, err := tw.Write(dockerfile); err != nil {
		c.Fatalf("failed to write tar file content: %v", err)
	}
	if err := tw.Close(); err != nil {
		c.Fatalf("failed to close tar archive: %v", err)
	}

	server := fakeBinaryStorage(c, map[string]*bytes.Buffer{
		"testT.tar": buffer,
	})
	defer server.Close()

	buildImageSuccessfully(c, name, withBuildContextPath(server.URL()+"/testT.tar"))

	res := inspectField(c, name, "Author")
	if res != "docker" {
		c.Fatalf("Maintainer should be docker, got %s", res)
	}
}

func (s *DockerSuite) TestBuildCleanupCmdOnEntrypoint(c *check.C) {
	name := "testbuildcmdcleanuponentrypoint"

	buildImageSuccessfully(c, name, withDockerfile(`FROM `+minimalBaseImage()+`
		CMD ["test"]
		ENTRYPOINT ["echo"]`))
	buildImageSuccessfully(c, name, withDockerfile(fmt.Sprintf(`FROM %s
		ENTRYPOINT ["cat"]`, name)))

	res := inspectField(c, name, "Config.Cmd")
	if res != "[]" {
		c.Fatalf("Cmd %s, expected nil", res)
	}
	res = inspectField(c, name, "Config.Entrypoint")
	if expected := "[cat]"; res != expected {
		c.Fatalf("Entrypoint %s, expected %s", res, expected)
	}
}

func (s *DockerSuite) TestBuildClearCmd(c *check.C) {
	name := "testbuildclearcmd"
	buildImageSuccessfully(c, name, withDockerfile(`FROM `+minimalBaseImage()+`
   ENTRYPOINT ["/bin/bash"]
   CMD []`))

	res := inspectFieldJSON(c, name, "Config.Cmd")
	if res != "[]" {
		c.Fatalf("Cmd %s, expected %s", res, "[]")
	}
}

func (s *DockerSuite) TestBuildEmptyCmd(c *check.C) {
	// Skip on Windows. Base image on Windows has a CMD set in the image.
	testRequires(c, DaemonIsLinux)

	name := "testbuildemptycmd"
	buildImageSuccessfully(c, name, withDockerfile("FROM "+minimalBaseImage()+"\nMAINTAINER quux\n"))

	res := inspectFieldJSON(c, name, "Config.Cmd")
	if res != "null" {
		c.Fatalf("Cmd %s, expected %s", res, "null")
	}
}

func (s *DockerSuite) TestBuildOnBuildOutput(c *check.C) {
	name := "testbuildonbuildparent"
	buildImageSuccessfully(c, name, withDockerfile("FROM busybox\nONBUILD RUN echo foo\n"))

	buildImage(name, withDockerfile("FROM "+name+"\nMAINTAINER quux\n")).Assert(c, icmd.Expected{
		Out: "# Executing 1 build trigger",
	})
}

// FIXME(vdemeester) should be a unit test
func (s *DockerSuite) TestBuildInvalidTag(c *check.C) {
	name := "abcd:" + stringutils.GenerateRandomAlphaOnlyString(200)
	buildImage(name, withDockerfile("FROM "+minimalBaseImage()+"\nMAINTAINER quux\n")).Assert(c, icmd.Expected{
		ExitCode: 125,
		Err:      "invalid reference format",
	})
}

func (s *DockerSuite) TestBuildCmdShDashC(c *check.C) {
	name := "testbuildcmdshc"
	buildImageSuccessfully(c, name, withDockerfile("FROM busybox\nCMD echo cmd\n"))

	res := inspectFieldJSON(c, name, "Config.Cmd")
	expected := `["/bin/sh","-c","echo cmd"]`
	if testEnv.DaemonPlatform() == "windows" {
		expected = `["cmd","/S","/C","echo cmd"]`
	}
	if res != expected {
		c.Fatalf("Expected value %s not in Config.Cmd: %s", expected, res)
	}

}

func (s *DockerSuite) TestBuildCmdSpaces(c *check.C) {
	// Test to make sure that when we strcat arrays we take into account
	// the arg separator to make sure ["echo","hi"] and ["echo hi"] don't
	// look the same
	name := "testbuildcmdspaces"

	buildImageSuccessfully(c, name, withDockerfile("FROM busybox\nCMD [\"echo hi\"]\n"))
	id1 := getIDByName(c, name)
	buildImageSuccessfully(c, name, withDockerfile("FROM busybox\nCMD [\"echo\", \"hi\"]\n"))
	id2 := getIDByName(c, name)

	if id1 == id2 {
		c.Fatal("Should not have resulted in the same CMD")
	}

	// Now do the same with ENTRYPOINT
	buildImageSuccessfully(c, name, withDockerfile("FROM busybox\nENTRYPOINT [\"echo hi\"]\n"))
	id1 = getIDByName(c, name)
	buildImageSuccessfully(c, name, withDockerfile("FROM busybox\nENTRYPOINT [\"echo\", \"hi\"]\n"))
	id2 = getIDByName(c, name)

	if id1 == id2 {
		c.Fatal("Should not have resulted in the same ENTRYPOINT")
	}
}

func (s *DockerSuite) TestBuildCmdJSONNoShDashC(c *check.C) {
	name := "testbuildcmdjson"
	buildImageSuccessfully(c, name, withDockerfile("FROM busybox\nCMD [\"echo\", \"cmd\"]"))

	res := inspectFieldJSON(c, name, "Config.Cmd")
	expected := `["echo","cmd"]`
	if res != expected {
		c.Fatalf("Expected value %s not in Config.Cmd: %s", expected, res)
	}
}

func (s *DockerSuite) TestBuildEntrypointCanBeOverridenByChild(c *check.C) {
	buildImageSuccessfully(c, "parent", withDockerfile(`
    FROM busybox
    ENTRYPOINT exit 130
    `))

	icmd.RunCommand(dockerBinary, "run", "parent").Assert(c, icmd.Expected{
		ExitCode: 130,
	})

	buildImageSuccessfully(c, "child", withDockerfile(`
    FROM parent
    ENTRYPOINT exit 5
    `))

	icmd.RunCommand(dockerBinary, "run", "child").Assert(c, icmd.Expected{
		ExitCode: 5,
	})
}

func (s *DockerSuite) TestBuildEntrypointCanBeOverridenByChildInspect(c *check.C) {
	var (
		name     = "testbuildepinherit"
		name2    = "testbuildepinherit2"
		expected = `["/bin/sh","-c","echo quux"]`
	)

	if testEnv.DaemonPlatform() == "windows" {
		expected = `["cmd","/S","/C","echo quux"]`
	}

	buildImageSuccessfully(c, name, withDockerfile("FROM busybox\nENTRYPOINT /foo/bar"))
	buildImageSuccessfully(c, name2, withDockerfile(fmt.Sprintf("FROM %s\nENTRYPOINT echo quux", name)))

	res := inspectFieldJSON(c, name2, "Config.Entrypoint")
	if res != expected {
		c.Fatalf("Expected value %s not in Config.Entrypoint: %s", expected, res)
	}

	icmd.RunCommand(dockerBinary, "run", name2).Assert(c, icmd.Expected{
		Out: "quux",
	})
}

func (s *DockerSuite) TestBuildRunShEntrypoint(c *check.C) {
	name := "testbuildentrypoint"
	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
                                ENTRYPOINT echo`))
	dockerCmd(c, "run", "--rm", name)
}

func (s *DockerSuite) TestBuildExoticShellInterpolation(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildexoticshellinterpolation"

	buildImageSuccessfully(c, name, withDockerfile(`
		FROM busybox

		ENV SOME_VAR a.b.c

		RUN [ "$SOME_VAR"       = 'a.b.c' ]
		RUN [ "${SOME_VAR}"     = 'a.b.c' ]
		RUN [ "${SOME_VAR%.*}"  = 'a.b'   ]
		RUN [ "${SOME_VAR%%.*}" = 'a'     ]
		RUN [ "${SOME_VAR#*.}"  = 'b.c'   ]
		RUN [ "${SOME_VAR##*.}" = 'c'     ]
		RUN [ "${SOME_VAR/c/d}" = 'a.b.d' ]
		RUN [ "${#SOME_VAR}"    = '5'     ]

		RUN [ "${SOME_UNSET_VAR:-$SOME_VAR}" = 'a.b.c' ]
		RUN [ "${SOME_VAR:+Version: ${SOME_VAR}}" = 'Version: a.b.c' ]
		RUN [ "${SOME_UNSET_VAR:+${SOME_VAR}}" = '' ]
		RUN [ "${SOME_UNSET_VAR:-${SOME_VAR:-d.e.f}}" = 'a.b.c' ]
	`))
}

func (s *DockerSuite) TestBuildVerifySingleQuoteFails(c *check.C) {
	// This testcase is supposed to generate an error because the
	// JSON array we're passing in on the CMD uses single quotes instead
	// of double quotes (per the JSON spec). This means we interpret it
	// as a "string" instead of "JSON array" and pass it on to "sh -c" and
	// it should barf on it.
	name := "testbuildsinglequotefails"
	expectedExitCode := 2
	if testEnv.DaemonPlatform() == "windows" {
		expectedExitCode = 127
	}

	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
		CMD [ '/bin/sh', '-c', 'echo hi' ]`))

	icmd.RunCommand(dockerBinary, "run", "--rm", name).Assert(c, icmd.Expected{
		ExitCode: expectedExitCode,
	})
}

func (s *DockerSuite) TestBuildVerboseOut(c *check.C) {
	name := "testbuildverboseout"
	expected := "\n123\n"

	if testEnv.DaemonPlatform() == "windows" {
		expected = "\n123\r\n"
	}

	buildImage(name, withDockerfile(`FROM busybox
RUN echo 123`)).Assert(c, icmd.Expected{
		Out: expected,
	})
}

func (s *DockerSuite) TestBuildWithTabs(c *check.C) {
	name := "testbuildwithtabs"
	buildImageSuccessfully(c, name, withDockerfile("FROM busybox\nRUN echo\tone\t\ttwo"))
	res := inspectFieldJSON(c, name, "ContainerConfig.Cmd")
	expected1 := `["/bin/sh","-c","echo\tone\t\ttwo"]`
	expected2 := `["/bin/sh","-c","echo\u0009one\u0009\u0009two"]` // syntactically equivalent, and what Go 1.3 generates
	if testEnv.DaemonPlatform() == "windows" {
		expected1 = `["cmd","/S","/C","echo\tone\t\ttwo"]`
		expected2 = `["cmd","/S","/C","echo\u0009one\u0009\u0009two"]` // syntactically equivalent, and what Go 1.3 generates
	}
	if res != expected1 && res != expected2 {
		c.Fatalf("Missing tabs.\nGot: %s\nExp: %s or %s", res, expected1, expected2)
	}
}

func (s *DockerSuite) TestBuildLabels(c *check.C) {
	name := "testbuildlabel"
	expected := `{"License":"GPL","Vendor":"Acme"}`
	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
		LABEL Vendor=Acme
                LABEL License GPL`))
	res := inspectFieldJSON(c, name, "Config.Labels")
	if res != expected {
		c.Fatalf("Labels %s, expected %s", res, expected)
	}
}

func (s *DockerSuite) TestBuildLabelsCache(c *check.C) {
	name := "testbuildlabelcache"

	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
		LABEL Vendor=Acme`))
	id1 := getIDByName(c, name)
	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
		LABEL Vendor=Acme`))
	id2 := getIDByName(c, name)
	if id1 != id2 {
		c.Fatalf("Build 2 should have worked & used cache(%s,%s)", id1, id2)
	}

	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
		LABEL Vendor=Acme1`))
	id2 = getIDByName(c, name)
	if id1 == id2 {
		c.Fatalf("Build 3 should have worked & NOT used cache(%s,%s)", id1, id2)
	}

	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
		LABEL Vendor Acme`))
	id2 = getIDByName(c, name)
	if id1 != id2 {
		c.Fatalf("Build 4 should have worked & used cache(%s,%s)", id1, id2)
	}

	// Now make sure the cache isn't used by mistake
	buildImageSuccessfully(c, name, withoutCache, withDockerfile(`FROM busybox
       LABEL f1=b1 f2=b2`))

	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
       LABEL f1=b1 f2=b2`))
	id2 = getIDByName(c, name)
	if id1 == id2 {
		c.Fatalf("Build 6 should have worked & NOT used the cache(%s,%s)", id1, id2)
	}

}

func (s *DockerSuite) TestBuildNotVerboseSuccess(c *check.C) {
	// This test makes sure that -q works correctly when build is successful:
	// stdout has only the image ID (long image ID) and stderr is empty.
	outRegexp := regexp.MustCompile("^(sha256:|)[a-z0-9]{64}\\n$")
	buildFlags := withBuildFlags("-q")

	tt := []struct {
		Name      string
		BuildFunc func(string) *icmd.Result
	}{
		{
			Name: "quiet_build_stdin_success",
			BuildFunc: func(name string) *icmd.Result {
				return buildImage(name, buildFlags, withDockerfile("FROM busybox"))
			},
		},
		{
			Name: "quiet_build_ctx_success",
			BuildFunc: func(name string) *icmd.Result {
				return buildImage(name, buildFlags, withBuildContext(c,
					withFile("Dockerfile", "FROM busybox"),
					withFile("quiet_build_success_fctx", "test"),
				))
			},
		},
		{
			Name: "quiet_build_git_success",
			BuildFunc: func(name string) *icmd.Result {
				git := newFakeGit(c, "repo", map[string]string{
					"Dockerfile": "FROM busybox",
				}, true)
				return buildImage(name, buildFlags, withBuildContextPath(git.RepoURL))
			},
		},
	}

	for _, te := range tt {
		result := te.BuildFunc(te.Name)
		result.Assert(c, icmd.Success)
		if outRegexp.Find([]byte(result.Stdout())) == nil {
			c.Fatalf("Test %s expected stdout to match the [%v] regexp, but it is [%v]", te.Name, outRegexp, result.Stdout())
		}

		if result.Stderr() != "" {
			c.Fatalf("Test %s expected stderr to be empty, but it is [%#v]", te.Name, result.Stderr())
		}
	}

}

func (s *DockerSuite) TestBuildNotVerboseFailureWithNonExistImage(c *check.C) {
	// This test makes sure that -q works correctly when build fails by
	// comparing between the stderr output in quiet mode and in stdout
	// and stderr output in verbose mode
	testRequires(c, Network)
	testName := "quiet_build_not_exists_image"
	dockerfile := "FROM busybox11"
	quietResult := buildImage(testName, withBuildFlags("-q"), withDockerfile(dockerfile))
	quietResult.Assert(c, icmd.Expected{
		ExitCode: 1,
	})
	result := buildImage(testName, withDockerfile(dockerfile))
	result.Assert(c, icmd.Expected{
		ExitCode: 1,
	})
	if quietResult.Stderr() != result.Combined() {
		c.Fatal(fmt.Errorf("Test[%s] expected that quiet stderr and verbose stdout are equal; quiet [%v], verbose [%v]", testName, quietResult.Stderr(), result.Combined()))
	}
}

func (s *DockerSuite) TestBuildNotVerboseFailure(c *check.C) {
	// This test makes sure that -q works correctly when build fails by
	// comparing between the stderr output in quiet mode and in stdout
	// and stderr output in verbose mode
	testCases := []struct {
		testName   string
		dockerfile string
	}{
		{"quiet_build_no_from_at_the_beginning", "RUN whoami"},
		{"quiet_build_unknown_instr", "FROMD busybox"},
	}

	for _, tc := range testCases {
		quietResult := buildImage(tc.testName, withBuildFlags("-q"), withDockerfile(tc.dockerfile))
		quietResult.Assert(c, icmd.Expected{
			ExitCode: 1,
		})
		result := buildImage(tc.testName, withDockerfile(tc.dockerfile))
		result.Assert(c, icmd.Expected{
			ExitCode: 1,
		})
		if quietResult.Stderr() != result.Combined() {
			c.Fatal(fmt.Errorf("Test[%s] expected that quiet stderr and verbose stdout are equal; quiet [%v], verbose [%v]", tc.testName, quietResult.Stderr(), result.Combined()))
		}
	}
}

func (s *DockerSuite) TestBuildNotVerboseFailureRemote(c *check.C) {
	// This test ensures that when given a wrong URL, stderr in quiet mode and
	// stderr in verbose mode are identical.
	// TODO(vdemeester) with cobra, stdout has a carriage return too much so this test should not check stdout
	URL := "http://something.invalid"
	name := "quiet_build_wrong_remote"
	quietResult := buildImage(name, withBuildFlags("-q"), withBuildContextPath(URL))
	quietResult.Assert(c, icmd.Expected{
		ExitCode: 1,
	})
	result := buildImage(name, withBuildContextPath(URL))
	result.Assert(c, icmd.Expected{
		ExitCode: 1,
	})
	if strings.TrimSpace(quietResult.Stderr()) != strings.TrimSpace(result.Combined()) {
		c.Fatal(fmt.Errorf("Test[%s] expected that quiet stderr and verbose stdout are equal; quiet [%v], verbose [%v]", name, quietResult.Stderr(), result.Combined()))
	}
}

func (s *DockerSuite) TestBuildStderr(c *check.C) {
	// This test just makes sure that no non-error output goes
	// to stderr
	name := "testbuildstderr"
	result := buildImage(name, withDockerfile("FROM busybox\nRUN echo one"))
	result.Assert(c, icmd.Success)

	// Windows to non-Windows should have a security warning
	if runtime.GOOS == "windows" && testEnv.DaemonPlatform() != "windows" && !strings.Contains(result.Stdout(), "SECURITY WARNING:") {
		c.Fatalf("Stdout contains unexpected output: %q", result.Stdout())
	}

	// Stderr should always be empty
	if result.Stderr() != "" {
		c.Fatalf("Stderr should have been empty, instead it's: %q", result.Stderr())
	}
}

func (s *DockerSuite) TestBuildChownSingleFile(c *check.C) {
	testRequires(c, UnixCli, DaemonIsLinux) // test uses chown: not available on windows

	name := "testbuildchownsinglefile"

	ctx := fakeContext(c, `
FROM busybox
COPY test /
RUN ls -l /test
RUN [ $(ls -l /test | awk '{print $3":"$4}') = 'root:root' ]
`, map[string]string{
		"test": "test",
	})
	defer ctx.Close()

	if err := os.Chown(filepath.Join(ctx.Dir, "test"), 4242, 4242); err != nil {
		c.Fatal(err)
	}

	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))
}

func (s *DockerSuite) TestBuildSymlinkBreakout(c *check.C) {
	name := "testbuildsymlinkbreakout"
	tmpdir, err := ioutil.TempDir("", name)
	c.Assert(err, check.IsNil)
	defer os.RemoveAll(tmpdir)
	ctx := filepath.Join(tmpdir, "context")
	if err := os.MkdirAll(ctx, 0755); err != nil {
		c.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(ctx, "Dockerfile"), []byte(`
	from busybox
	add symlink.tar /
	add inject /symlink/
	`), 0644); err != nil {
		c.Fatal(err)
	}
	inject := filepath.Join(ctx, "inject")
	if err := ioutil.WriteFile(inject, nil, 0644); err != nil {
		c.Fatal(err)
	}
	f, err := os.Create(filepath.Join(ctx, "symlink.tar"))
	if err != nil {
		c.Fatal(err)
	}
	w := tar.NewWriter(f)
	w.WriteHeader(&tar.Header{
		Name:     "symlink2",
		Typeflag: tar.TypeSymlink,
		Linkname: "/../../../../../../../../../../../../../../",
		Uid:      os.Getuid(),
		Gid:      os.Getgid(),
	})
	w.WriteHeader(&tar.Header{
		Name:     "symlink",
		Typeflag: tar.TypeSymlink,
		Linkname: filepath.Join("symlink2", tmpdir),
		Uid:      os.Getuid(),
		Gid:      os.Getgid(),
	})
	w.Close()
	f.Close()

	buildImageSuccessfully(c, name, withoutCache, withExternalBuildContext(fakeContextFromDir(ctx)))
	if _, err := os.Lstat(filepath.Join(tmpdir, "inject")); err == nil {
		c.Fatal("symlink breakout - inject")
	} else if !os.IsNotExist(err) {
		c.Fatalf("unexpected error: %v", err)
	}
}

func (s *DockerSuite) TestBuildXZHost(c *check.C) {
	// /usr/local/sbin/xz gets permission denied for the user
	testRequires(c, NotUserNamespace)
	testRequires(c, DaemonIsLinux)
	name := "testbuildxzhost"

	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", `
FROM busybox
ADD xz /usr/local/sbin/
RUN chmod 755 /usr/local/sbin/xz
ADD test.xz /
RUN [ ! -e /injected ]`),
		withFile("test.xz", "\xfd\x37\x7a\x58\x5a\x00\x00\x04\xe6\xd6\xb4\x46\x02\x00"+
			"\x21\x01\x16\x00\x00\x00\x74\x2f\xe5\xa3\x01\x00\x3f\xfd"+
			"\x37\x7a\x58\x5a\x00\x00\x04\xe6\xd6\xb4\x46\x02\x00\x21"),
		withFile("xz", "#!/bin/sh\ntouch /injected"),
	))
}

func (s *DockerSuite) TestBuildVolumesRetainContents(c *check.C) {
	// /foo/file gets permission denied for the user
	testRequires(c, NotUserNamespace)
	testRequires(c, DaemonIsLinux) // TODO Windows: Issue #20127
	var (
		name     = "testbuildvolumescontent"
		expected = "some text"
		volName  = "/foo"
	)

	if testEnv.DaemonPlatform() == "windows" {
		volName = "C:/foo"
	}

	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", `
FROM busybox
COPY content /foo/file
VOLUME `+volName+`
CMD cat /foo/file`),
		withFile("content", expected),
	))

	out, _ := dockerCmd(c, "run", "--rm", name)
	if out != expected {
		c.Fatalf("expected file contents for /foo/file to be %q but received %q", expected, out)
	}

}

// FIXME(vdemeester) part of this should be unit test, other part should be clearer
func (s *DockerSuite) TestBuildRenamedDockerfile(c *check.C) {
	ctx := fakeContext(c, `FROM busybox
	RUN echo from Dockerfile`,
		map[string]string{
			"Dockerfile":       "FROM busybox\nRUN echo from Dockerfile",
			"files/Dockerfile": "FROM busybox\nRUN echo from files/Dockerfile",
			"files/dFile":      "FROM busybox\nRUN echo from files/dFile",
			"dFile":            "FROM busybox\nRUN echo from dFile",
			"files/dFile2":     "FROM busybox\nRUN echo from files/dFile2",
		})
	defer ctx.Close()

	out, _, err := dockerCmdInDir(c, ctx.Dir, "build", "-t", "test1", ".")
	if err != nil {
		c.Fatalf("Failed to build: %s\n%s", out, err)
	}
	if !strings.Contains(out, "from Dockerfile") {
		c.Fatalf("test1 should have used Dockerfile, output:%s", out)
	}

	out, _, err = dockerCmdInDir(c, ctx.Dir, "build", "-f", filepath.Join("files", "Dockerfile"), "-t", "test2", ".")
	if err != nil {
		c.Fatal(err)
	}
	if !strings.Contains(out, "from files/Dockerfile") {
		c.Fatalf("test2 should have used files/Dockerfile, output:%s", out)
	}

	out, _, err = dockerCmdInDir(c, ctx.Dir, "build", fmt.Sprintf("--file=%s", filepath.Join("files", "dFile")), "-t", "test3", ".")
	if err != nil {
		c.Fatal(err)
	}
	if !strings.Contains(out, "from files/dFile") {
		c.Fatalf("test3 should have used files/dFile, output:%s", out)
	}

	out, _, err = dockerCmdInDir(c, ctx.Dir, "build", "--file=dFile", "-t", "test4", ".")
	if err != nil {
		c.Fatal(err)
	}
	if !strings.Contains(out, "from dFile") {
		c.Fatalf("test4 should have used dFile, output:%s", out)
	}

	dirWithNoDockerfile, err := ioutil.TempDir(os.TempDir(), "test5")
	c.Assert(err, check.IsNil)
	nonDockerfileFile := filepath.Join(dirWithNoDockerfile, "notDockerfile")
	if _, err = os.Create(nonDockerfileFile); err != nil {
		c.Fatal(err)
	}
	out, _, err = dockerCmdInDir(c, ctx.Dir, "build", fmt.Sprintf("--file=%s", nonDockerfileFile), "-t", "test5", ".")

	if err == nil {
		c.Fatalf("test5 was supposed to fail to find passwd")
	}

	if expected := fmt.Sprintf("The Dockerfile (%s) must be within the build context (.)", nonDockerfileFile); !strings.Contains(out, expected) {
		c.Fatalf("wrong error message:%v\nexpected to contain=%v", out, expected)
	}

	out, _, err = dockerCmdInDir(c, filepath.Join(ctx.Dir, "files"), "build", "-f", filepath.Join("..", "Dockerfile"), "-t", "test6", "..")
	if err != nil {
		c.Fatalf("test6 failed: %s", err)
	}
	if !strings.Contains(out, "from Dockerfile") {
		c.Fatalf("test6 should have used root Dockerfile, output:%s", out)
	}

	out, _, err = dockerCmdInDir(c, filepath.Join(ctx.Dir, "files"), "build", "-f", filepath.Join(ctx.Dir, "files", "Dockerfile"), "-t", "test7", "..")
	if err != nil {
		c.Fatalf("test7 failed: %s", err)
	}
	if !strings.Contains(out, "from files/Dockerfile") {
		c.Fatalf("test7 should have used files Dockerfile, output:%s", out)
	}

	out, _, err = dockerCmdInDir(c, filepath.Join(ctx.Dir, "files"), "build", "-f", filepath.Join("..", "Dockerfile"), "-t", "test8", ".")
	if err == nil || !strings.Contains(out, "must be within the build context") {
		c.Fatalf("test8 should have failed with Dockerfile out of context: %s", err)
	}

	tmpDir := os.TempDir()
	out, _, err = dockerCmdInDir(c, tmpDir, "build", "-t", "test9", ctx.Dir)
	if err != nil {
		c.Fatalf("test9 - failed: %s", err)
	}
	if !strings.Contains(out, "from Dockerfile") {
		c.Fatalf("test9 should have used root Dockerfile, output:%s", out)
	}

	out, _, err = dockerCmdInDir(c, filepath.Join(ctx.Dir, "files"), "build", "-f", "dFile2", "-t", "test10", ".")
	if err != nil {
		c.Fatalf("test10 should have worked: %s", err)
	}
	if !strings.Contains(out, "from files/dFile2") {
		c.Fatalf("test10 should have used files/dFile2, output:%s", out)
	}

}

func (s *DockerSuite) TestBuildFromMixedcaseDockerfile(c *check.C) {
	testRequires(c, UnixCli) // Dockerfile overwrites dockerfile on windows
	testRequires(c, DaemonIsLinux)

	// If Dockerfile is not present, use dockerfile
	buildImage("test1", withBuildContext(c,
		withFile("dockerfile", `FROM busybox
	RUN echo from dockerfile`),
	)).Assert(c, icmd.Expected{
		Out: "from dockerfile",
	})

	// Prefer Dockerfile in place of dockerfile
	buildImage("test1", withBuildContext(c,
		withFile("dockerfile", `FROM busybox
	RUN echo from dockerfile`),
		withFile("Dockerfile", `FROM busybox
	RUN echo from Dockerfile`),
	)).Assert(c, icmd.Expected{
		Out: "from Dockerfile",
	})
}

func (s *DockerSuite) TestBuildFromURLWithF(c *check.C) {
	server := fakeStorage(c, map[string]string{"baz": `FROM busybox
RUN echo from baz
COPY * /tmp/
RUN find /tmp/`})
	defer server.Close()

	ctx := fakeContext(c, `FROM busybox
RUN echo from Dockerfile`,
		map[string]string{})
	defer ctx.Close()

	// Make sure that -f is ignored and that we don't use the Dockerfile
	// that's in the current dir
	result := buildImage("test1", withBuildFlags("-f", "baz", server.URL()+"/baz"), func(cmd *icmd.Cmd) func() {
		cmd.Dir = ctx.Dir
		return nil
	})
	result.Assert(c, icmd.Success)

	if !strings.Contains(result.Combined(), "from baz") ||
		strings.Contains(result.Combined(), "/tmp/baz") ||
		!strings.Contains(result.Combined(), "/tmp/Dockerfile") {
		c.Fatalf("Missing proper output: %s", result.Combined())
	}

}

func (s *DockerSuite) TestBuildFromStdinWithF(c *check.C) {
	testRequires(c, DaemonIsLinux) // TODO Windows: This test is flaky; no idea why
	ctx := fakeContext(c, `FROM busybox
RUN echo "from Dockerfile"`,
		map[string]string{})
	defer ctx.Close()

	// Make sure that -f is ignored and that we don't use the Dockerfile
	// that's in the current dir
	result := buildImage("test1", withBuildFlags("-f", "baz", "-"), func(cmd *icmd.Cmd) func() {
		cmd.Dir = ctx.Dir
		cmd.Stdin = strings.NewReader(`FROM busybox
RUN echo "from baz"
COPY * /tmp/
RUN sh -c "find /tmp/" # sh -c is needed on Windows to use the correct find`)
		return nil
	})
	result.Assert(c, icmd.Success)

	if !strings.Contains(result.Combined(), "from baz") ||
		strings.Contains(result.Combined(), "/tmp/baz") ||
		!strings.Contains(result.Combined(), "/tmp/Dockerfile") {
		c.Fatalf("Missing proper output: %s", result.Combined())
	}

}

func (s *DockerSuite) TestBuildFromOfficialNames(c *check.C) {
	name := "testbuildfromofficial"
	fromNames := []string{
		"busybox",
		"docker.io/busybox",
		"index.docker.io/busybox",
		"library/busybox",
		"docker.io/library/busybox",
		"index.docker.io/library/busybox",
	}
	for idx, fromName := range fromNames {
		imgName := fmt.Sprintf("%s%d", name, idx)
		buildImageSuccessfully(c, imgName, withDockerfile("FROM "+fromName))
		dockerCmd(c, "rmi", imgName)
	}
}

func (s *DockerSuite) TestBuildDockerfileOutsideContext(c *check.C) {
	testRequires(c, UnixCli, DaemonIsLinux) // uses os.Symlink: not implemented in windows at the time of writing (go-1.4.2)

	name := "testbuilddockerfileoutsidecontext"
	tmpdir, err := ioutil.TempDir("", name)
	c.Assert(err, check.IsNil)
	defer os.RemoveAll(tmpdir)
	ctx := filepath.Join(tmpdir, "context")
	if err := os.MkdirAll(ctx, 0755); err != nil {
		c.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(ctx, "Dockerfile"), []byte("FROM scratch\nENV X Y"), 0644); err != nil {
		c.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		c.Fatal(err)
	}
	defer os.Chdir(wd)
	if err := os.Chdir(ctx); err != nil {
		c.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(tmpdir, "outsideDockerfile"), []byte("FROM scratch\nENV x y"), 0644); err != nil {
		c.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "outsideDockerfile"), filepath.Join(ctx, "dockerfile1")); err != nil {
		c.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tmpdir, "outsideDockerfile"), filepath.Join(ctx, "dockerfile2")); err != nil {
		c.Fatal(err)
	}

	for _, dockerfilePath := range []string{
		filepath.Join("..", "outsideDockerfile"),
		filepath.Join(ctx, "dockerfile1"),
		filepath.Join(ctx, "dockerfile2"),
	} {
		result := dockerCmdWithResult("build", "-t", name, "--no-cache", "-f", dockerfilePath, ".")
		c.Assert(result, icmd.Matches, icmd.Expected{
			Err:      "must be within the build context",
			ExitCode: 1,
		})
		deleteImages(name)
	}

	os.Chdir(tmpdir)

	// Path to Dockerfile should be resolved relative to working directory, not relative to context.
	// There is a Dockerfile in the context, but since there is no Dockerfile in the current directory, the following should fail
	out, _, err := dockerCmdWithError("build", "-t", name, "--no-cache", "-f", "Dockerfile", ctx)
	if err == nil {
		c.Fatalf("Expected error. Out: %s", out)
	}
}

// FIXME(vdemeester) should be a unit test
func (s *DockerSuite) TestBuildSpaces(c *check.C) {
	// Test to make sure that leading/trailing spaces on a command
	// doesn't change the error msg we get
	name := "testspaces"
	ctx := fakeContext(c, "FROM busybox\nCOPY\n",
		map[string]string{
			"Dockerfile": "FROM busybox\nCOPY\n",
		})
	defer ctx.Close()

	result1 := buildImage(name, withExternalBuildContext(ctx))
	result1.Assert(c, icmd.Expected{
		ExitCode: 1,
	})

	ctx.Add("Dockerfile", "FROM busybox\nCOPY    ")
	result2 := buildImage(name, withExternalBuildContext(ctx))
	result2.Assert(c, icmd.Expected{
		ExitCode: 1,
	})

	removeLogTimestamps := func(s string) string {
		return regexp.MustCompile(`time="(.*?)"`).ReplaceAllString(s, `time=[TIMESTAMP]`)
	}

	// Skip over the times
	e1 := removeLogTimestamps(result1.Error.Error())
	e2 := removeLogTimestamps(result2.Error.Error())

	// Ignore whitespace since that's what were verifying doesn't change stuff
	if strings.Replace(e1, " ", "", -1) != strings.Replace(e2, " ", "", -1) {
		c.Fatalf("Build 2's error wasn't the same as build 1's\n1:%s\n2:%s", result1.Error, result2.Error)
	}

	ctx.Add("Dockerfile", "FROM busybox\n   COPY")
	result2 = buildImage(name, withoutCache, withExternalBuildContext(ctx))
	result2.Assert(c, icmd.Expected{
		ExitCode: 1,
	})

	// Skip over the times
	e1 = removeLogTimestamps(result1.Error.Error())
	e2 = removeLogTimestamps(result2.Error.Error())

	// Ignore whitespace since that's what were verifying doesn't change stuff
	if strings.Replace(e1, " ", "", -1) != strings.Replace(e2, " ", "", -1) {
		c.Fatalf("Build 3's error wasn't the same as build 1's\n1:%s\n3:%s", result1.Error, result2.Error)
	}

	ctx.Add("Dockerfile", "FROM busybox\n   COPY    ")
	result2 = buildImage(name, withoutCache, withExternalBuildContext(ctx))
	result2.Assert(c, icmd.Expected{
		ExitCode: 1,
	})

	// Skip over the times
	e1 = removeLogTimestamps(result1.Error.Error())
	e2 = removeLogTimestamps(result2.Error.Error())

	// Ignore whitespace since that's what were verifying doesn't change stuff
	if strings.Replace(e1, " ", "", -1) != strings.Replace(e2, " ", "", -1) {
		c.Fatalf("Build 4's error wasn't the same as build 1's\n1:%s\n4:%s", result1.Error, result2.Error)
	}

}

func (s *DockerSuite) TestBuildSpacesWithQuotes(c *check.C) {
	// Test to make sure that spaces in quotes aren't lost
	name := "testspacesquotes"

	dockerfile := `FROM busybox
RUN echo "  \
  foo  "`

	expected := "\n    foo  \n"
	// Windows uses the builtin echo, which preserves quotes
	if testEnv.DaemonPlatform() == "windows" {
		expected = "\"    foo  \""
	}

	buildImage(name, withDockerfile(dockerfile)).Assert(c, icmd.Expected{
		Out: expected,
	})
}

// #4393
func (s *DockerSuite) TestBuildVolumeFileExistsinContainer(c *check.C) {
	testRequires(c, DaemonIsLinux) // TODO Windows: This should error out
	buildImage("docker-test-errcreatevolumewithfile", withDockerfile(`
	FROM busybox
	RUN touch /foo
	VOLUME /foo
	`)).Assert(c, icmd.Expected{
		ExitCode: 1,
		Err:      "file exists",
	})
}

// FIXME(vdemeester) should be a unit test
func (s *DockerSuite) TestBuildMissingArgs(c *check.C) {
	// Test to make sure that all Dockerfile commands (except the ones listed
	// in skipCmds) will generate an error if no args are provided.
	// Note: INSERT is deprecated so we exclude it because of that.
	skipCmds := map[string]struct{}{
		"CMD":        {},
		"RUN":        {},
		"ENTRYPOINT": {},
		"INSERT":     {},
	}

	if testEnv.DaemonPlatform() == "windows" {
		skipCmds = map[string]struct{}{
			"CMD":        {},
			"RUN":        {},
			"ENTRYPOINT": {},
			"INSERT":     {},
			"STOPSIGNAL": {},
			"ARG":        {},
			"USER":       {},
			"EXPOSE":     {},
		}
	}

	for cmd := range command.Commands {
		cmd = strings.ToUpper(cmd)
		if _, ok := skipCmds[cmd]; ok {
			continue
		}
		var dockerfile string
		if cmd == "FROM" {
			dockerfile = cmd
		} else {
			// Add FROM to make sure we don't complain about it missing
			dockerfile = "FROM busybox\n" + cmd
		}

		buildImage("args", withDockerfile(dockerfile)).Assert(c, icmd.Expected{
			ExitCode: 1,
			Err:      cmd + " requires",
		})
	}

}

func (s *DockerSuite) TestBuildEmptyScratch(c *check.C) {
	testRequires(c, DaemonIsLinux)
	buildImage("sc", withDockerfile("FROM scratch")).Assert(c, icmd.Expected{
		ExitCode: 1,
		Err:      "No image was generated",
	})
}

func (s *DockerSuite) TestBuildDotDotFile(c *check.C) {
	buildImageSuccessfully(c, "sc", withBuildContext(c,
		withFile("Dockerfile", "FROM busybox\n"),
		withFile("..gitme", ""),
	))
}

func (s *DockerSuite) TestBuildRUNoneJSON(c *check.C) {
	testRequires(c, DaemonIsLinux) // No hello-world Windows image
	name := "testbuildrunonejson"

	buildImage(name, withDockerfile(`FROM hello-world:frozen
RUN [ "/hello" ]`)).Assert(c, icmd.Expected{
		Out: "Hello from Docker",
	})
}

func (s *DockerSuite) TestBuildEmptyStringVolume(c *check.C) {
	name := "testbuildemptystringvolume"

	buildImage(name, withDockerfile(`
  FROM busybox
  ENV foo=""
  VOLUME $foo
  `)).Assert(c, icmd.Expected{
		ExitCode: 1,
	})
}

func (s *DockerSuite) TestBuildContainerWithCgroupParent(c *check.C) {
	testRequires(c, SameHostDaemon, DaemonIsLinux)

	cgroupParent := "test"
	data, err := ioutil.ReadFile("/proc/self/cgroup")
	if err != nil {
		c.Fatalf("failed to read '/proc/self/cgroup - %v", err)
	}
	selfCgroupPaths := testutil.ParseCgroupPaths(string(data))
	_, found := selfCgroupPaths["memory"]
	if !found {
		c.Fatalf("unable to find self memory cgroup path. CgroupsPath: %v", selfCgroupPaths)
	}
	result := buildImage("buildcgroupparent",
		withBuildFlags("--cgroup-parent", cgroupParent),
		withDockerfile(`
FROM busybox
RUN cat /proc/self/cgroup
`))
	result.Assert(c, icmd.Success)
	m, err := regexp.MatchString(fmt.Sprintf("memory:.*/%s/.*", cgroupParent), result.Combined())
	c.Assert(err, check.IsNil)
	if !m {
		c.Fatalf("There is no expected memory cgroup with parent /%s/: %s", cgroupParent, result.Combined())
	}
}

// FIXME(vdemeester) could be a unit test
func (s *DockerSuite) TestBuildNoDupOutput(c *check.C) {
	// Check to make sure our build output prints the Dockerfile cmd
	// property - there was a bug that caused it to be duplicated on the
	// Step X  line
	name := "testbuildnodupoutput"
	result := buildImage(name, withDockerfile(`
  FROM busybox
  RUN env`))
	result.Assert(c, icmd.Success)
	exp := "\nStep 2/2 : RUN env\n"
	if !strings.Contains(result.Combined(), exp) {
		c.Fatalf("Bad output\nGot:%s\n\nExpected to contain:%s\n", result.Combined(), exp)
	}
}

// GH15826
// FIXME(vdemeester) could be a unit test
func (s *DockerSuite) TestBuildStartsFromOne(c *check.C) {
	// Explicit check to ensure that build starts from step 1 rather than 0
	name := "testbuildstartsfromone"
	result := buildImage(name, withDockerfile(`FROM busybox`))
	result.Assert(c, icmd.Success)
	exp := "\nStep 1/1 : FROM busybox\n"
	if !strings.Contains(result.Combined(), exp) {
		c.Fatalf("Bad output\nGot:%s\n\nExpected to contain:%s\n", result.Combined(), exp)
	}
}

func (s *DockerSuite) TestBuildRUNErrMsg(c *check.C) {
	// Test to make sure the bad command is quoted with just "s and
	// not as a Go []string
	name := "testbuildbadrunerrmsg"
	shell := "/bin/sh -c"
	exitCode := 127
	if testEnv.DaemonPlatform() == "windows" {
		shell = "cmd /S /C"
		// architectural - Windows has to start the container to determine the exe is bad, Linux does not
		exitCode = 1
	}
	exp := fmt.Sprintf(`The command '%s badEXE a1 \& a2	a3' returned a non-zero code: %d`, shell, exitCode)

	buildImage(name, withDockerfile(`
  FROM busybox
  RUN badEXE a1 \& a2	a3`)).Assert(c, icmd.Expected{
		ExitCode: exitCode,
		Err:      exp,
	})
}

func (s *DockerTrustSuite) TestTrustedBuild(c *check.C) {
	repoName := s.setupTrustedImage(c, "trusted-build")
	dockerFile := fmt.Sprintf(`
  FROM %s
  RUN []
    `, repoName)

	name := "testtrustedbuild"

	buildImage(name, trustedBuild, withDockerfile(dockerFile)).Assert(c, icmd.Expected{
		Out: fmt.Sprintf("FROM %s@sha", repoName[:len(repoName)-7]),
	})

	// We should also have a tag reference for the image.
	dockerCmd(c, "inspect", repoName)

	// We should now be able to remove the tag reference.
	dockerCmd(c, "rmi", repoName)
}

func (s *DockerTrustSuite) TestTrustedBuildUntrustedTag(c *check.C) {
	repoName := fmt.Sprintf("%v/dockercli/build-untrusted-tag:latest", privateRegistryURL)
	dockerFile := fmt.Sprintf(`
  FROM %s
  RUN []
    `, repoName)

	name := "testtrustedbuilduntrustedtag"

	buildImage(name, trustedBuild, withDockerfile(dockerFile)).Assert(c, icmd.Expected{
		ExitCode: 1,
		Err:      "does not have trust data for",
	})
}

func (s *DockerTrustSuite) TestBuildContextDirIsSymlink(c *check.C) {
	testRequires(c, DaemonIsLinux)
	tempDir, err := ioutil.TempDir("", "test-build-dir-is-symlink-")
	c.Assert(err, check.IsNil)
	defer os.RemoveAll(tempDir)

	// Make a real context directory in this temp directory with a simple
	// Dockerfile.
	realContextDirname := filepath.Join(tempDir, "context")
	if err := os.Mkdir(realContextDirname, os.FileMode(0755)); err != nil {
		c.Fatal(err)
	}

	if err = ioutil.WriteFile(
		filepath.Join(realContextDirname, "Dockerfile"),
		[]byte(`
			FROM busybox
			RUN echo hello world
		`),
		os.FileMode(0644),
	); err != nil {
		c.Fatal(err)
	}

	// Make a symlink to the real context directory.
	contextSymlinkName := filepath.Join(tempDir, "context_link")
	if err := os.Symlink(realContextDirname, contextSymlinkName); err != nil {
		c.Fatal(err)
	}

	// Executing the build with the symlink as the specified context should
	// *not* fail.
	dockerCmd(c, "build", contextSymlinkName)
}

func (s *DockerTrustSuite) TestTrustedBuildTagFromReleasesRole(c *check.C) {
	testRequires(c, NotaryHosting)

	latestTag := s.setupTrustedImage(c, "trusted-build-releases-role")
	repoName := strings.TrimSuffix(latestTag, ":latest")

	// Now create the releases role
	s.notaryCreateDelegation(c, repoName, "targets/releases", s.not.keys[0].Public)
	s.notaryImportKey(c, repoName, "targets/releases", s.not.keys[0].Private)
	s.notaryPublish(c, repoName)

	// push a different tag to the releases role
	otherTag := fmt.Sprintf("%s:other", repoName)
	dockerCmd(c, "tag", "busybox", otherTag)

	icmd.RunCmd(icmd.Command(dockerBinary, "push", otherTag), trustedCmd).Assert(c, icmd.Success)
	s.assertTargetInRoles(c, repoName, "other", "targets/releases")
	s.assertTargetNotInRoles(c, repoName, "other", "targets")

	out, status := dockerCmd(c, "rmi", otherTag)
	c.Assert(status, check.Equals, 0, check.Commentf("docker rmi failed: %s", out))

	dockerFile := fmt.Sprintf(`
  FROM %s
  RUN []
    `, otherTag)
	name := "testtrustedbuildreleasesrole"
	buildImage(name, trustedBuild, withDockerfile(dockerFile)).Assert(c, icmd.Expected{
		Out: fmt.Sprintf("FROM %s@sha", repoName),
	})
}

func (s *DockerTrustSuite) TestTrustedBuildTagIgnoresOtherDelegationRoles(c *check.C) {
	testRequires(c, NotaryHosting)

	latestTag := s.setupTrustedImage(c, "trusted-build-releases-role")
	repoName := strings.TrimSuffix(latestTag, ":latest")

	// Now create a non-releases delegation role
	s.notaryCreateDelegation(c, repoName, "targets/other", s.not.keys[0].Public)
	s.notaryImportKey(c, repoName, "targets/other", s.not.keys[0].Private)
	s.notaryPublish(c, repoName)

	// push a different tag to the other role
	otherTag := fmt.Sprintf("%s:other", repoName)
	dockerCmd(c, "tag", "busybox", otherTag)

	icmd.RunCmd(icmd.Command(dockerBinary, "push", otherTag), trustedCmd).Assert(c, icmd.Success)
	s.assertTargetInRoles(c, repoName, "other", "targets/other")
	s.assertTargetNotInRoles(c, repoName, "other", "targets")

	out, status := dockerCmd(c, "rmi", otherTag)
	c.Assert(status, check.Equals, 0, check.Commentf("docker rmi failed: %s", out))

	dockerFile := fmt.Sprintf(`
  FROM %s
  RUN []
    `, otherTag)

	name := "testtrustedbuildotherrole"
	buildImage(name, trustedBuild, withDockerfile(dockerFile)).Assert(c, icmd.Expected{
		ExitCode: 1,
	})
}

// Issue #15634: COPY fails when path starts with "null"
func (s *DockerSuite) TestBuildNullStringInAddCopyVolume(c *check.C) {
	name := "testbuildnullstringinaddcopyvolume"
	volName := "nullvolume"
	if testEnv.DaemonPlatform() == "windows" {
		volName = `C:\\nullvolume`
	}

	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", `
		FROM busybox

		ADD null /
		COPY nullfile /
		VOLUME `+volName+`
		`),
		withFile("null", "test1"),
		withFile("nullfile", "test2"),
	))
}

func (s *DockerSuite) TestBuildStopSignal(c *check.C) {
	testRequires(c, DaemonIsLinux) // Windows does not support STOPSIGNAL yet
	imgName := "test_build_stop_signal"
	buildImageSuccessfully(c, imgName, withDockerfile(`FROM busybox
		 STOPSIGNAL SIGKILL`))
	res := inspectFieldJSON(c, imgName, "Config.StopSignal")
	if res != `"SIGKILL"` {
		c.Fatalf("Signal %s, expected SIGKILL", res)
	}

	containerName := "test-container-stop-signal"
	dockerCmd(c, "run", "-d", "--name", containerName, imgName, "top")
	res = inspectFieldJSON(c, containerName, "Config.StopSignal")
	if res != `"SIGKILL"` {
		c.Fatalf("Signal %s, expected SIGKILL", res)
	}
}

func (s *DockerSuite) TestBuildBuildTimeArg(c *check.C) {
	imgName := "bldargtest"
	envKey := "foo"
	envVal := "bar"
	var dockerfile string
	if testEnv.DaemonPlatform() == "windows" {
		// Bugs in Windows busybox port - use the default base image and native cmd stuff
		dockerfile = fmt.Sprintf(`FROM `+minimalBaseImage()+`
			ARG %s
			RUN echo %%%s%%
			CMD setlocal enableextensions && if defined %s (echo %%%s%%)`, envKey, envKey, envKey, envKey)
	} else {
		dockerfile = fmt.Sprintf(`FROM busybox
			ARG %s
			RUN echo $%s
			CMD echo $%s`, envKey, envKey, envKey)

	}
	buildImage(imgName,
		withBuildFlags("--build-arg", fmt.Sprintf("%s=%s", envKey, envVal)),
		withDockerfile(dockerfile),
	).Assert(c, icmd.Expected{
		Out: envVal,
	})

	containerName := "bldargCont"
	out, _ := dockerCmd(c, "run", "--name", containerName, imgName)
	out = strings.Trim(out, " \r\n'")
	if out != "" {
		c.Fatalf("run produced invalid output: %q, expected empty string", out)
	}
}

func (s *DockerSuite) TestBuildBuildTimeArgHistory(c *check.C) {
	imgName := "bldargtest"
	envKey := "foo"
	envVal := "bar"
	envDef := "bar1"
	dockerfile := fmt.Sprintf(`FROM busybox
		ARG %s=%s`, envKey, envDef)
	buildImage(imgName,
		withBuildFlags("--build-arg", fmt.Sprintf("%s=%s", envKey, envVal)),
		withDockerfile(dockerfile),
	).Assert(c, icmd.Expected{
		Out: envVal,
	})

	out, _ := dockerCmd(c, "history", "--no-trunc", imgName)
	outputTabs := strings.Split(out, "\n")[1]
	if !strings.Contains(outputTabs, envDef) {
		c.Fatalf("failed to find arg default in image history output: %q expected: %q", outputTabs, envDef)
	}
}

func (s *DockerSuite) TestBuildBuildTimeArgCacheHit(c *check.C) {
	imgName := "bldargtest"
	envKey := "foo"
	envVal := "bar"
	dockerfile := fmt.Sprintf(`FROM busybox
		ARG %s
		RUN echo $%s`, envKey, envKey)
	buildImageSuccessfully(c, imgName,
		withBuildFlags("--build-arg", fmt.Sprintf("%s=%s", envKey, envVal)),
		withDockerfile(dockerfile),
	)
	origImgID := getIDByName(c, imgName)

	imgNameCache := "bldargtestcachehit"
	buildImageSuccessfully(c, imgNameCache,
		withBuildFlags("--build-arg", fmt.Sprintf("%s=%s", envKey, envVal)),
		withDockerfile(dockerfile),
	)
	newImgID := getIDByName(c, imgName)
	if newImgID != origImgID {
		c.Fatalf("build didn't use cache! expected image id: %q built image id: %q", origImgID, newImgID)
	}
}

func (s *DockerSuite) TestBuildBuildTimeArgCacheMissExtraArg(c *check.C) {
	imgName := "bldargtest"
	envKey := "foo"
	envVal := "bar"
	extraEnvKey := "foo1"
	extraEnvVal := "bar1"
	dockerfile := fmt.Sprintf(`FROM busybox
		ARG %s
		ARG %s
		RUN echo $%s`, envKey, extraEnvKey, envKey)
	buildImageSuccessfully(c, imgName,
		withBuildFlags("--build-arg", fmt.Sprintf("%s=%s", envKey, envVal)),
		withDockerfile(dockerfile),
	)
	origImgID := getIDByName(c, imgName)

	imgNameCache := "bldargtestcachemiss"
	buildImageSuccessfully(c, imgNameCache,
		withBuildFlags(
			"--build-arg", fmt.Sprintf("%s=%s", envKey, envVal),
			"--build-arg", fmt.Sprintf("%s=%s", extraEnvKey, extraEnvVal),
		),
		withDockerfile(dockerfile),
	)
	newImgID := getIDByName(c, imgNameCache)

	if newImgID == origImgID {
		c.Fatalf("build used cache, expected a miss!")
	}
}

func (s *DockerSuite) TestBuildBuildTimeArgCacheMissSameArgDiffVal(c *check.C) {
	imgName := "bldargtest"
	envKey := "foo"
	envVal := "bar"
	newEnvVal := "bar1"
	dockerfile := fmt.Sprintf(`FROM busybox
		ARG %s
		RUN echo $%s`, envKey, envKey)
	buildImageSuccessfully(c, imgName,
		withBuildFlags("--build-arg", fmt.Sprintf("%s=%s", envKey, envVal)),
		withDockerfile(dockerfile),
	)
	origImgID := getIDByName(c, imgName)

	imgNameCache := "bldargtestcachemiss"
	buildImageSuccessfully(c, imgNameCache,
		withBuildFlags("--build-arg", fmt.Sprintf("%s=%s", envKey, newEnvVal)),
		withDockerfile(dockerfile),
	)
	newImgID := getIDByName(c, imgNameCache)
	if newImgID == origImgID {
		c.Fatalf("build used cache, expected a miss!")
	}
}

func (s *DockerSuite) TestBuildBuildTimeArgOverrideArgDefinedBeforeEnv(c *check.C) {
	testRequires(c, DaemonIsLinux) // Windows does not support ARG
	imgName := "bldargtest"
	envKey := "foo"
	envVal := "bar"
	envValOveride := "barOverride"
	dockerfile := fmt.Sprintf(`FROM busybox
		ARG %s
		ENV %s %s
		RUN echo $%s
		CMD echo $%s
        `, envKey, envKey, envValOveride, envKey, envKey)

	result := buildImage(imgName,
		withBuildFlags("--build-arg", fmt.Sprintf("%s=%s", envKey, envVal)),
		withDockerfile(dockerfile),
	)
	result.Assert(c, icmd.Success)
	if strings.Count(result.Combined(), envValOveride) != 2 {
		c.Fatalf("failed to access environment variable in output: %q expected: %q", result.Combined(), envValOveride)
	}

	containerName := "bldargCont"
	if out, _ := dockerCmd(c, "run", "--name", containerName, imgName); !strings.Contains(out, envValOveride) {
		c.Fatalf("run produced invalid output: %q, expected %q", out, envValOveride)
	}
}

// FIXME(vdemeester) might be useful to merge with the one above ?
func (s *DockerSuite) TestBuildBuildTimeArgOverrideEnvDefinedBeforeArg(c *check.C) {
	testRequires(c, DaemonIsLinux) // Windows does not support ARG
	imgName := "bldargtest"
	envKey := "foo"
	envVal := "bar"
	envValOveride := "barOverride"
	dockerfile := fmt.Sprintf(`FROM busybox
		ENV %s %s
		ARG %s
		RUN echo $%s
		CMD echo $%s
        `, envKey, envValOveride, envKey, envKey, envKey)
	result := buildImage(imgName,
		withBuildFlags("--build-arg", fmt.Sprintf("%s=%s", envKey, envVal)),
		withDockerfile(dockerfile),
	)
	result.Assert(c, icmd.Success)
	if strings.Count(result.Combined(), envValOveride) != 2 {
		c.Fatalf("failed to access environment variable in output: %q expected: %q", result.Combined(), envValOveride)
	}

	containerName := "bldargCont"
	if out, _ := dockerCmd(c, "run", "--name", containerName, imgName); !strings.Contains(out, envValOveride) {
		c.Fatalf("run produced invalid output: %q, expected %q", out, envValOveride)
	}
}

func (s *DockerSuite) TestBuildBuildTimeArgExpansion(c *check.C) {
	testRequires(c, DaemonIsLinux) // Windows does not support ARG
	imgName := "bldvarstest"

	wdVar := "WDIR"
	wdVal := "/tmp/"
	addVar := "AFILE"
	addVal := "addFile"
	copyVar := "CFILE"
	copyVal := "copyFile"
	envVar := "foo"
	envVal := "bar"
	exposeVar := "EPORT"
	exposeVal := "9999"
	userVar := "USER"
	userVal := "testUser"
	volVar := "VOL"
	volVal := "/testVol/"

	buildImageSuccessfully(c, imgName,
		withBuildFlags(
			"--build-arg", fmt.Sprintf("%s=%s", wdVar, wdVal),
			"--build-arg", fmt.Sprintf("%s=%s", addVar, addVal),
			"--build-arg", fmt.Sprintf("%s=%s", copyVar, copyVal),
			"--build-arg", fmt.Sprintf("%s=%s", envVar, envVal),
			"--build-arg", fmt.Sprintf("%s=%s", exposeVar, exposeVal),
			"--build-arg", fmt.Sprintf("%s=%s", userVar, userVal),
			"--build-arg", fmt.Sprintf("%s=%s", volVar, volVal),
		),
		withBuildContext(c,
			withFile("Dockerfile", fmt.Sprintf(`FROM busybox
		ARG %s
		WORKDIR ${%s}
		ARG %s
		ADD ${%s} testDir/
		ARG %s
		COPY $%s testDir/
		ARG %s
		ENV %s=${%s}
		ARG %s
		EXPOSE $%s
		ARG %s
		USER $%s
		ARG %s
		VOLUME ${%s}`,
				wdVar, wdVar, addVar, addVar, copyVar, copyVar, envVar, envVar,
				envVar, exposeVar, exposeVar, userVar, userVar, volVar, volVar)),
			withFile(addVal, "some stuff"),
			withFile(copyVal, "some stuff"),
		),
	)

	res := inspectField(c, imgName, "Config.WorkingDir")
	if res != filepath.ToSlash(filepath.Clean(wdVal)) {
		c.Fatalf("Config.WorkingDir value mismatch. Expected: %s, got: %s", filepath.ToSlash(filepath.Clean(wdVal)), res)
	}

	var resArr []string
	inspectFieldAndUnmarshall(c, imgName, "Config.Env", &resArr)

	found := false
	for _, v := range resArr {
		if fmt.Sprintf("%s=%s", envVar, envVal) == v {
			found = true
			break
		}
	}
	if !found {
		c.Fatalf("Config.Env value mismatch. Expected <key=value> to exist: %s=%s, got: %v",
			envVar, envVal, resArr)
	}

	var resMap map[string]interface{}
	inspectFieldAndUnmarshall(c, imgName, "Config.ExposedPorts", &resMap)
	if _, ok := resMap[fmt.Sprintf("%s/tcp", exposeVal)]; !ok {
		c.Fatalf("Config.ExposedPorts value mismatch. Expected exposed port: %s/tcp, got: %v", exposeVal, resMap)
	}

	res = inspectField(c, imgName, "Config.User")
	if res != userVal {
		c.Fatalf("Config.User value mismatch. Expected: %s, got: %s", userVal, res)
	}

	inspectFieldAndUnmarshall(c, imgName, "Config.Volumes", &resMap)
	if _, ok := resMap[volVal]; !ok {
		c.Fatalf("Config.Volumes value mismatch. Expected volume: %s, got: %v", volVal, resMap)
	}
}

func (s *DockerSuite) TestBuildBuildTimeArgExpansionOverride(c *check.C) {
	testRequires(c, DaemonIsLinux) // Windows does not support ARG
	imgName := "bldvarstest"
	envKey := "foo"
	envVal := "bar"
	envKey1 := "foo1"
	envValOveride := "barOverride"
	dockerfile := fmt.Sprintf(`FROM busybox
		ARG %s
		ENV %s %s
		ENV %s ${%s}
		RUN echo $%s
		CMD echo $%s`, envKey, envKey, envValOveride, envKey1, envKey, envKey1, envKey1)
	result := buildImage(imgName,
		withBuildFlags("--build-arg", fmt.Sprintf("%s=%s", envKey, envVal)),
		withDockerfile(dockerfile),
	)
	result.Assert(c, icmd.Success)
	if strings.Count(result.Combined(), envValOveride) != 2 {
		c.Fatalf("failed to access environment variable in output: %q expected: %q", result.Combined(), envValOveride)
	}

	containerName := "bldargCont"
	if out, _ := dockerCmd(c, "run", "--name", containerName, imgName); !strings.Contains(out, envValOveride) {
		c.Fatalf("run produced invalid output: %q, expected %q", out, envValOveride)
	}
}

func (s *DockerSuite) TestBuildBuildTimeArgUntrustedDefinedAfterUse(c *check.C) {
	testRequires(c, DaemonIsLinux) // Windows does not support ARG
	imgName := "bldargtest"
	envKey := "foo"
	envVal := "bar"
	dockerfile := fmt.Sprintf(`FROM busybox
		RUN echo $%s
		ARG %s
		CMD echo $%s`, envKey, envKey, envKey)
	result := buildImage(imgName,
		withBuildFlags("--build-arg", fmt.Sprintf("%s=%s", envKey, envVal)),
		withDockerfile(dockerfile),
	)
	result.Assert(c, icmd.Success)
	if strings.Contains(result.Combined(), envVal) {
		c.Fatalf("able to access environment variable in output: %q expected to be missing", result.Combined())
	}

	containerName := "bldargCont"
	if out, _ := dockerCmd(c, "run", "--name", containerName, imgName); out != "\n" {
		c.Fatalf("run produced invalid output: %q, expected empty string", out)
	}
}

func (s *DockerSuite) TestBuildBuildTimeArgBuiltinArg(c *check.C) {
	testRequires(c, DaemonIsLinux) // Windows does not support --build-arg
	imgName := "bldargtest"
	envKey := "HTTP_PROXY"
	envVal := "bar"
	dockerfile := fmt.Sprintf(`FROM busybox
		RUN echo $%s
		CMD echo $%s`, envKey, envKey)

	result := buildImage(imgName,
		withBuildFlags("--build-arg", fmt.Sprintf("%s=%s", envKey, envVal)),
		withDockerfile(dockerfile),
	)
	result.Assert(c, icmd.Success)
	if !strings.Contains(result.Combined(), envVal) {
		c.Fatalf("failed to access environment variable in output: %q expected: %q", result.Combined(), envVal)
	}
	containerName := "bldargCont"
	if out, _ := dockerCmd(c, "run", "--name", containerName, imgName); out != "\n" {
		c.Fatalf("run produced invalid output: %q, expected empty string", out)
	}
}

func (s *DockerSuite) TestBuildBuildTimeArgDefaultOverride(c *check.C) {
	testRequires(c, DaemonIsLinux) // Windows does not support ARG
	imgName := "bldargtest"
	envKey := "foo"
	envVal := "bar"
	envValOveride := "barOverride"
	dockerfile := fmt.Sprintf(`FROM busybox
		ARG %s=%s
		ENV %s $%s
		RUN echo $%s
		CMD echo $%s`, envKey, envVal, envKey, envKey, envKey, envKey)
	result := buildImage(imgName,
		withBuildFlags("--build-arg", fmt.Sprintf("%s=%s", envKey, envValOveride)),
		withDockerfile(dockerfile),
	)
	result.Assert(c, icmd.Success)
	if strings.Count(result.Combined(), envValOveride) != 1 {
		c.Fatalf("failed to access environment variable in output: %q expected: %q", result.Combined(), envValOveride)
	}

	containerName := "bldargCont"
	if out, _ := dockerCmd(c, "run", "--name", containerName, imgName); !strings.Contains(out, envValOveride) {
		c.Fatalf("run produced invalid output: %q, expected %q", out, envValOveride)
	}
}

func (s *DockerSuite) TestBuildBuildTimeArgUnconsumedArg(c *check.C) {
	imgName := "bldargtest"
	envKey := "foo"
	envVal := "bar"
	dockerfile := fmt.Sprintf(`FROM busybox
		RUN echo $%s
		CMD echo $%s`, envKey, envKey)
	warnStr := "[Warning] One or more build-args"
	buildImage(imgName,
		withBuildFlags("--build-arg", fmt.Sprintf("%s=%s", envKey, envVal)),
		withDockerfile(dockerfile),
	).Assert(c, icmd.Expected{
		Out: warnStr,
	})
}

func (s *DockerSuite) TestBuildBuildTimeArgEnv(c *check.C) {
	testRequires(c, DaemonIsLinux) // Windows does not support ARG
	dockerfile := `FROM busybox
		ARG FOO1=fromfile
		ARG FOO2=fromfile
		ARG FOO3=fromfile
		ARG FOO4=fromfile
		ARG FOO5
		ARG FOO6
		RUN env
		RUN [ "$FOO1" == "fromcmd" ]
		RUN [ "$FOO2" == "" ]
		RUN [ "$FOO3" == "fromenv" ]
		RUN [ "$FOO4" == "fromfile" ]
		RUN [ "$FOO5" == "fromcmd" ]
		# The following should not exist at all in the env
		RUN [ "$(env | grep FOO6)" == "" ]
		RUN [ "$(env | grep FOO7)" == "" ]
		RUN [ "$(env | grep FOO8)" == "" ]
		RUN [ "$(env | grep FOO9)" == "" ]
	    `
	result := buildImage("testbuildtimeargenv",
		withBuildFlags(
			"--build-arg", fmt.Sprintf("FOO1=fromcmd"),
			"--build-arg", fmt.Sprintf("FOO2="),
			"--build-arg", fmt.Sprintf("FOO3"), // set in env
			"--build-arg", fmt.Sprintf("FOO4"), // not set in env
			"--build-arg", fmt.Sprintf("FOO5=fromcmd"),
			// FOO6 is not set at all
			"--build-arg", fmt.Sprintf("FOO7=fromcmd"), // should produce a warning
			"--build-arg", fmt.Sprintf("FOO8="), // should produce a warning
			"--build-arg", fmt.Sprintf("FOO9"), // should produce a warning
		),
		withEnvironmentVariales(append(os.Environ(),
			"FOO1=fromenv",
			"FOO2=fromenv",
			"FOO3=fromenv")...),
		withBuildContext(c,
			withFile("Dockerfile", dockerfile),
		),
	)
	result.Assert(c, icmd.Success)

	// Now check to make sure we got a warning msg about unused build-args
	i := strings.Index(result.Combined(), "[Warning]")
	if i < 0 {
		c.Fatalf("Missing the build-arg warning in %q", result.Combined())
	}

	out := result.Combined()[i:] // "out" should contain just the warning message now

	// These were specified on a --build-arg but no ARG was in the Dockerfile
	c.Assert(out, checker.Contains, "FOO7")
	c.Assert(out, checker.Contains, "FOO8")
	c.Assert(out, checker.Contains, "FOO9")
}

func (s *DockerSuite) TestBuildBuildTimeArgQuotedValVariants(c *check.C) {
	imgName := "bldargtest"
	envKey := "foo"
	envKey1 := "foo1"
	envKey2 := "foo2"
	envKey3 := "foo3"
	dockerfile := fmt.Sprintf(`FROM busybox
		ARG %s=""
		ARG %s=''
		ARG %s="''"
		ARG %s='""'
		RUN [ "$%s" != "$%s" ]
		RUN [ "$%s" != "$%s" ]
		RUN [ "$%s" != "$%s" ]
		RUN [ "$%s" != "$%s" ]
		RUN [ "$%s" != "$%s" ]`, envKey, envKey1, envKey2, envKey3,
		envKey, envKey2, envKey, envKey3, envKey1, envKey2, envKey1, envKey3,
		envKey2, envKey3)
	buildImageSuccessfully(c, imgName, withDockerfile(dockerfile))
}

func (s *DockerSuite) TestBuildBuildTimeArgEmptyValVariants(c *check.C) {
	testRequires(c, DaemonIsLinux) // Windows does not support ARG
	imgName := "bldargtest"
	envKey := "foo"
	envKey1 := "foo1"
	envKey2 := "foo2"
	dockerfile := fmt.Sprintf(`FROM busybox
		ARG %s=
		ARG %s=""
		ARG %s=''
		RUN [ "$%s" == "$%s" ]
		RUN [ "$%s" == "$%s" ]
		RUN [ "$%s" == "$%s" ]`, envKey, envKey1, envKey2, envKey, envKey1, envKey1, envKey2, envKey, envKey2)
	buildImageSuccessfully(c, imgName, withDockerfile(dockerfile))
}

func (s *DockerSuite) TestBuildBuildTimeArgDefintionWithNoEnvInjection(c *check.C) {
	imgName := "bldargtest"
	envKey := "foo"
	dockerfile := fmt.Sprintf(`FROM busybox
		ARG %s
		RUN env`, envKey)

	result := buildImage(imgName, withDockerfile(dockerfile))
	result.Assert(c, icmd.Success)
	if strings.Count(result.Combined(), envKey) != 1 {
		c.Fatalf("unexpected number of occurrences of the arg in output: %q expected: 1", result.Combined())
	}
}

func (s *DockerSuite) TestBuildBuildTimeArgMultipleFrom(c *check.C) {
	imgName := "multifrombldargtest"
	dockerfile := `FROM busybox
    ARG foo=abc
    LABEL multifromtest=1
    RUN env > /out
    FROM busybox
    ARG bar=def
    RUN env > /out`

	result := buildImage(imgName, withDockerfile(dockerfile))
	result.Assert(c, icmd.Success)

	result = icmd.RunCmd(icmd.Cmd{
		Command: []string{dockerBinary, "images", "-q", "-f", "label=multifromtest=1"},
	})
	result.Assert(c, icmd.Success)
	parentID := strings.TrimSpace(result.Stdout())

	result = icmd.RunCmd(icmd.Cmd{
		Command: []string{dockerBinary, "run", "--rm", parentID, "cat", "/out"},
	})
	result.Assert(c, icmd.Success)
	c.Assert(result.Stdout(), checker.Contains, "foo=abc")

	result = icmd.RunCmd(icmd.Cmd{
		Command: []string{dockerBinary, "run", "--rm", imgName, "cat", "/out"},
	})
	result.Assert(c, icmd.Success)
	c.Assert(result.Stdout(), checker.Not(checker.Contains), "foo")
	c.Assert(result.Stdout(), checker.Contains, "bar=def")
}

func (s *DockerSuite) TestBuildBuildTimeUnusedArgMultipleFrom(c *check.C) {
	imgName := "multifromunusedarg"
	dockerfile := `FROM busybox
    ARG foo
    FROM busybox
    ARG bar
    RUN env > /out`

	result := buildImage(imgName, withDockerfile(dockerfile), withBuildFlags(
		"--build-arg", fmt.Sprintf("baz=abc")))
	result.Assert(c, icmd.Success)
	c.Assert(result.Combined(), checker.Contains, "[Warning]")
	c.Assert(result.Combined(), checker.Contains, "[baz] were not consumed")

	result = icmd.RunCmd(icmd.Cmd{
		Command: []string{dockerBinary, "run", "--rm", imgName, "cat", "/out"},
	})
	result.Assert(c, icmd.Success)
	c.Assert(result.Stdout(), checker.Not(checker.Contains), "bar")
	c.Assert(result.Stdout(), checker.Not(checker.Contains), "baz")
}

func (s *DockerSuite) TestBuildNoNamedVolume(c *check.C) {
	volName := "testname:/foo"

	if testEnv.DaemonPlatform() == "windows" {
		volName = "testname:C:\\foo"
	}
	dockerCmd(c, "run", "-v", volName, "busybox", "sh", "-c", "touch /foo/oops")

	dockerFile := `FROM busybox
	VOLUME ` + volName + `
	RUN ls /foo/oops
	`
	buildImage("test", withDockerfile(dockerFile)).Assert(c, icmd.Expected{
		ExitCode: 1,
	})
}

func (s *DockerSuite) TestBuildTagEvent(c *check.C) {
	since := daemonUnixTime(c)

	dockerFile := `FROM busybox
	RUN echo events
	`
	buildImageSuccessfully(c, "test", withDockerfile(dockerFile))

	until := daemonUnixTime(c)
	out, _ := dockerCmd(c, "events", "--since", since, "--until", until, "--filter", "type=image")
	events := strings.Split(strings.TrimSpace(out), "\n")
	actions := eventActionsByIDAndType(c, events, "test:latest", "image")
	var foundTag bool
	for _, a := range actions {
		if a == "tag" {
			foundTag = true
			break
		}
	}

	c.Assert(foundTag, checker.True, check.Commentf("No tag event found:\n%s", out))
}

// #15780
func (s *DockerSuite) TestBuildMultipleTags(c *check.C) {
	dockerfile := `
	FROM busybox
	MAINTAINER test-15780
	`
	buildImageSuccessfully(c, "tag1", withBuildFlags("-t", "tag2:v2", "-t", "tag1:latest", "-t", "tag1"), withDockerfile(dockerfile))

	id1 := getIDByName(c, "tag1")
	id2 := getIDByName(c, "tag2:v2")
	c.Assert(id1, check.Equals, id2)
}

// #17290
func (s *DockerSuite) TestBuildCacheBrokenSymlink(c *check.C) {
	name := "testbuildbrokensymlink"
	ctx := fakeContext(c, `
	FROM busybox
	COPY . ./`,
		map[string]string{
			"foo": "bar",
		})
	defer ctx.Close()

	err := os.Symlink(filepath.Join(ctx.Dir, "nosuchfile"), filepath.Join(ctx.Dir, "asymlink"))
	c.Assert(err, checker.IsNil)

	// warm up cache
	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))

	// add new file to context, should invalidate cache
	err = ioutil.WriteFile(filepath.Join(ctx.Dir, "newfile"), []byte("foo"), 0644)
	c.Assert(err, checker.IsNil)

	result := buildImage(name, withExternalBuildContext(ctx))
	result.Assert(c, icmd.Success)
	if strings.Contains(result.Combined(), "Using cache") {
		c.Fatal("2nd build used cache on ADD, it shouldn't")
	}
}

func (s *DockerSuite) TestBuildFollowSymlinkToFile(c *check.C) {
	name := "testbuildbrokensymlink"
	ctx := fakeContext(c, `
	FROM busybox
	COPY asymlink target`,
		map[string]string{
			"foo": "bar",
		})
	defer ctx.Close()

	err := os.Symlink("foo", filepath.Join(ctx.Dir, "asymlink"))
	c.Assert(err, checker.IsNil)

	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))

	out, _ := dockerCmd(c, "run", "--rm", name, "cat", "target")
	c.Assert(out, checker.Matches, "bar")

	// change target file should invalidate cache
	err = ioutil.WriteFile(filepath.Join(ctx.Dir, "foo"), []byte("baz"), 0644)
	c.Assert(err, checker.IsNil)

	result := buildImage(name, withExternalBuildContext(ctx))
	result.Assert(c, icmd.Success)
	c.Assert(result.Combined(), checker.Not(checker.Contains), "Using cache")

	out, _ = dockerCmd(c, "run", "--rm", name, "cat", "target")
	c.Assert(out, checker.Matches, "baz")
}

func (s *DockerSuite) TestBuildFollowSymlinkToDir(c *check.C) {
	name := "testbuildbrokensymlink"
	ctx := fakeContext(c, `
	FROM busybox
	COPY asymlink /`,
		map[string]string{
			"foo/abc": "bar",
			"foo/def": "baz",
		})
	defer ctx.Close()

	err := os.Symlink("foo", filepath.Join(ctx.Dir, "asymlink"))
	c.Assert(err, checker.IsNil)

	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))

	out, _ := dockerCmd(c, "run", "--rm", name, "cat", "abc", "def")
	c.Assert(out, checker.Matches, "barbaz")

	// change target file should invalidate cache
	err = ioutil.WriteFile(filepath.Join(ctx.Dir, "foo/def"), []byte("bax"), 0644)
	c.Assert(err, checker.IsNil)

	result := buildImage(name, withExternalBuildContext(ctx))
	result.Assert(c, icmd.Success)
	c.Assert(result.Combined(), checker.Not(checker.Contains), "Using cache")

	out, _ = dockerCmd(c, "run", "--rm", name, "cat", "abc", "def")
	c.Assert(out, checker.Matches, "barbax")

}

// TestBuildSymlinkBasename tests that target file gets basename from symlink,
// not from the target file.
func (s *DockerSuite) TestBuildSymlinkBasename(c *check.C) {
	name := "testbuildbrokensymlink"
	ctx := fakeContext(c, `
	FROM busybox
	COPY asymlink /`,
		map[string]string{
			"foo": "bar",
		})
	defer ctx.Close()

	err := os.Symlink("foo", filepath.Join(ctx.Dir, "asymlink"))
	c.Assert(err, checker.IsNil)

	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))

	out, _ := dockerCmd(c, "run", "--rm", name, "cat", "asymlink")
	c.Assert(out, checker.Matches, "bar")
}

// #17827
func (s *DockerSuite) TestBuildCacheRootSource(c *check.C) {
	name := "testbuildrootsource"
	ctx := fakeContext(c, `
	FROM busybox
	COPY / /data`,
		map[string]string{
			"foo": "bar",
		})
	defer ctx.Close()

	// warm up cache
	buildImageSuccessfully(c, name, withExternalBuildContext(ctx))

	// change file, should invalidate cache
	err := ioutil.WriteFile(filepath.Join(ctx.Dir, "foo"), []byte("baz"), 0644)
	c.Assert(err, checker.IsNil)

	result := buildImage(name, withExternalBuildContext(ctx))
	result.Assert(c, icmd.Success)

	c.Assert(result.Combined(), checker.Not(checker.Contains), "Using cache")
}

// #19375
func (s *DockerSuite) TestBuildFailsGitNotCallable(c *check.C) {
	buildImage("gitnotcallable", withEnvironmentVariales("PATH="),
		withBuildContextPath("github.com/docker/v1.10-migrator.git")).Assert(c, icmd.Expected{
		ExitCode: 1,
		Err:      "unable to prepare context: unable to find 'git': ",
	})

	buildImage("gitnotcallable", withEnvironmentVariales("PATH="),
		withBuildContextPath("https://github.com/docker/v1.10-migrator.git")).Assert(c, icmd.Expected{
		ExitCode: 1,
		Err:      "unable to prepare context: unable to find 'git': ",
	})
}

// TestBuildWorkdirWindowsPath tests that a Windows style path works as a workdir
func (s *DockerSuite) TestBuildWorkdirWindowsPath(c *check.C) {
	testRequires(c, DaemonIsWindows)
	name := "testbuildworkdirwindowspath"
	buildImageSuccessfully(c, name, withDockerfile(`
	FROM `+testEnv.MinimalBaseImage()+`
	RUN mkdir C:\\work
	WORKDIR C:\\work
	RUN if "%CD%" NEQ "C:\work" exit -1
	`))
}

func (s *DockerSuite) TestBuildLabel(c *check.C) {
	name := "testbuildlabel"
	testLabel := "foo"

	buildImageSuccessfully(c, name, withBuildFlags("--label", testLabel),
		withDockerfile(`
  FROM `+minimalBaseImage()+`
  LABEL default foo
`))

	var labels map[string]string
	inspectFieldAndUnmarshall(c, name, "Config.Labels", &labels)
	if _, ok := labels[testLabel]; !ok {
		c.Fatal("label not found in image")
	}
}

func (s *DockerSuite) TestBuildLabelOneNode(c *check.C) {
	name := "testbuildlabel"
	buildImageSuccessfully(c, name, withBuildFlags("--label", "foo=bar"),
		withDockerfile("FROM busybox"))

	var labels map[string]string
	inspectFieldAndUnmarshall(c, name, "Config.Labels", &labels)
	v, ok := labels["foo"]
	if !ok {
		c.Fatal("label `foo` not found in image")
	}
	c.Assert(v, checker.Equals, "bar")
}

func (s *DockerSuite) TestBuildLabelCacheCommit(c *check.C) {
	name := "testbuildlabelcachecommit"
	testLabel := "foo"

	buildImageSuccessfully(c, name, withDockerfile(`
  FROM `+minimalBaseImage()+`
  LABEL default foo
  `))
	buildImageSuccessfully(c, name, withBuildFlags("--label", testLabel),
		withDockerfile(`
  FROM `+minimalBaseImage()+`
  LABEL default foo
  `))

	var labels map[string]string
	inspectFieldAndUnmarshall(c, name, "Config.Labels", &labels)
	if _, ok := labels[testLabel]; !ok {
		c.Fatal("label not found in image")
	}
}

func (s *DockerSuite) TestBuildLabelMultiple(c *check.C) {
	name := "testbuildlabelmultiple"
	testLabels := map[string]string{
		"foo": "bar",
		"123": "456",
	}
	labelArgs := []string{}
	for k, v := range testLabels {
		labelArgs = append(labelArgs, "--label", k+"="+v)
	}

	buildImageSuccessfully(c, name, withBuildFlags(labelArgs...),
		withDockerfile(`
  FROM `+minimalBaseImage()+`
  LABEL default foo
`))

	var labels map[string]string
	inspectFieldAndUnmarshall(c, name, "Config.Labels", &labels)
	for k, v := range testLabels {
		if x, ok := labels[k]; !ok || x != v {
			c.Fatalf("label %s=%s not found in image", k, v)
		}
	}
}

func (s *DockerRegistryAuthHtpasswdSuite) TestBuildFromAuthenticatedRegistry(c *check.C) {
	dockerCmd(c, "login", "-u", s.reg.Username(), "-p", s.reg.Password(), privateRegistryURL)
	baseImage := privateRegistryURL + "/baseimage"

	buildImageSuccessfully(c, baseImage, withDockerfile(`
	FROM busybox
	ENV env1 val1
	`))

	dockerCmd(c, "push", baseImage)
	dockerCmd(c, "rmi", baseImage)

	buildImageSuccessfully(c, baseImage, withDockerfile(fmt.Sprintf(`
	FROM %s
	ENV env2 val2
	`, baseImage)))
}

func (s *DockerRegistryAuthHtpasswdSuite) TestBuildWithExternalAuth(c *check.C) {
	osPath := os.Getenv("PATH")
	defer os.Setenv("PATH", osPath)

	workingDir, err := os.Getwd()
	c.Assert(err, checker.IsNil)
	absolute, err := filepath.Abs(filepath.Join(workingDir, "fixtures", "auth"))
	c.Assert(err, checker.IsNil)
	testPath := fmt.Sprintf("%s%c%s", osPath, filepath.ListSeparator, absolute)

	os.Setenv("PATH", testPath)

	repoName := fmt.Sprintf("%v/dockercli/busybox:authtest", privateRegistryURL)

	tmp, err := ioutil.TempDir("", "integration-cli-")
	c.Assert(err, checker.IsNil)

	externalAuthConfig := `{ "credsStore": "shell-test" }`

	configPath := filepath.Join(tmp, "config.json")
	err = ioutil.WriteFile(configPath, []byte(externalAuthConfig), 0644)
	c.Assert(err, checker.IsNil)

	dockerCmd(c, "--config", tmp, "login", "-u", s.reg.Username(), "-p", s.reg.Password(), privateRegistryURL)

	b, err := ioutil.ReadFile(configPath)
	c.Assert(err, checker.IsNil)
	c.Assert(string(b), checker.Not(checker.Contains), "\"auth\":")

	dockerCmd(c, "--config", tmp, "tag", "busybox", repoName)
	dockerCmd(c, "--config", tmp, "push", repoName)

	// make sure the image is pulled when building
	dockerCmd(c, "rmi", repoName)

	icmd.RunCmd(icmd.Cmd{
		Command: []string{dockerBinary, "--config", tmp, "build", "-"},
		Stdin:   strings.NewReader(fmt.Sprintf("FROM %s", repoName)),
	}).Assert(c, icmd.Success)
}

// Test cases in #22036
func (s *DockerSuite) TestBuildLabelsOverride(c *check.C) {
	// Command line option labels will always override
	name := "scratchy"
	expected := `{"bar":"from-flag","foo":"from-flag"}`
	buildImageSuccessfully(c, name, withBuildFlags("--label", "foo=from-flag", "--label", "bar=from-flag"),
		withDockerfile(`FROM `+minimalBaseImage()+`
                LABEL foo=from-dockerfile`))
	res := inspectFieldJSON(c, name, "Config.Labels")
	if res != expected {
		c.Fatalf("Labels %s, expected %s", res, expected)
	}

	name = "from"
	expected = `{"foo":"from-dockerfile"}`
	buildImageSuccessfully(c, name, withDockerfile(`FROM `+minimalBaseImage()+`
                LABEL foo from-dockerfile`))
	res = inspectFieldJSON(c, name, "Config.Labels")
	if res != expected {
		c.Fatalf("Labels %s, expected %s", res, expected)
	}

	// Command line option label will override even via `FROM`
	name = "new"
	expected = `{"bar":"from-dockerfile2","foo":"new"}`
	buildImageSuccessfully(c, name, withBuildFlags("--label", "foo=new"),
		withDockerfile(`FROM from
                LABEL bar from-dockerfile2`))
	res = inspectFieldJSON(c, name, "Config.Labels")
	if res != expected {
		c.Fatalf("Labels %s, expected %s", res, expected)
	}

	// Command line option without a value set (--label foo, --label bar=)
	// will be treated as --label foo="", --label bar=""
	name = "scratchy2"
	expected = `{"bar":"","foo":""}`
	buildImageSuccessfully(c, name, withBuildFlags("--label", "foo", "--label", "bar="),
		withDockerfile(`FROM `+minimalBaseImage()+`
                LABEL foo=from-dockerfile`))
	res = inspectFieldJSON(c, name, "Config.Labels")
	if res != expected {
		c.Fatalf("Labels %s, expected %s", res, expected)
	}

	// Command line option without a value set (--label foo, --label bar=)
	// will be treated as --label foo="", --label bar=""
	// This time is for inherited images
	name = "new2"
	expected = `{"bar":"","foo":""}`
	buildImageSuccessfully(c, name, withBuildFlags("--label", "foo=", "--label", "bar"),
		withDockerfile(`FROM from
                LABEL bar from-dockerfile2`))
	res = inspectFieldJSON(c, name, "Config.Labels")
	if res != expected {
		c.Fatalf("Labels %s, expected %s", res, expected)
	}

	// Command line option labels with only `FROM`
	name = "scratchy"
	expected = `{"bar":"from-flag","foo":"from-flag"}`
	buildImageSuccessfully(c, name, withBuildFlags("--label", "foo=from-flag", "--label", "bar=from-flag"),
		withDockerfile(`FROM `+minimalBaseImage()))
	res = inspectFieldJSON(c, name, "Config.Labels")
	if res != expected {
		c.Fatalf("Labels %s, expected %s", res, expected)
	}

	// Command line option labels with env var
	name = "scratchz"
	expected = `{"bar":"$PATH"}`
	buildImageSuccessfully(c, name, withBuildFlags("--label", "bar=$PATH"),
		withDockerfile(`FROM `+minimalBaseImage()))
	res = inspectFieldJSON(c, name, "Config.Labels")
	if res != expected {
		c.Fatalf("Labels %s, expected %s", res, expected)
	}
}

// Test case for #22855
func (s *DockerSuite) TestBuildDeleteCommittedFile(c *check.C) {
	name := "test-delete-committed-file"
	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
		RUN echo test > file
		RUN test -e file
		RUN rm file
		RUN sh -c "! test -e file"`))
}

// #20083
func (s *DockerSuite) TestBuildDockerignoreComment(c *check.C) {
	// TODO Windows: Figure out why this test is flakey on TP5. If you add
	// something like RUN sleep 5, or even RUN ls /tmp after the ADD line,
	// it is more reliable, but that's not a good fix.
	testRequires(c, DaemonIsLinux)

	name := "testbuilddockerignorecleanpaths"
	dockerfile := `
        FROM busybox
        ADD . /tmp/
        RUN sh -c "(ls -la /tmp/#1)"
        RUN sh -c "(! ls -la /tmp/#2)"
        RUN sh -c "(! ls /tmp/foo) && (! ls /tmp/foo2) && (ls /tmp/dir1/foo)"`
	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", dockerfile),
		withFile("foo", "foo"),
		withFile("foo2", "foo2"),
		withFile("dir1/foo", "foo in dir1"),
		withFile("#1", "# file 1"),
		withFile("#2", "# file 2"),
		withFile(".dockerignore", `# Visual C++ cache files
# because we have git ;-)
# The above comment is from #20083
foo
#dir1/foo
foo2
# The following is considered as comment as # is at the beginning
#1
# The following is not considered as comment as # is not at the beginning
  #2
`)))
}

// Test case for #23221
func (s *DockerSuite) TestBuildWithUTF8BOM(c *check.C) {
	name := "test-with-utf8-bom"
	dockerfile := []byte(`FROM busybox`)
	bomDockerfile := append([]byte{0xEF, 0xBB, 0xBF}, dockerfile...)
	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", string(bomDockerfile)),
	))
}

// Test case for UTF-8 BOM in .dockerignore, related to #23221
func (s *DockerSuite) TestBuildWithUTF8BOMDockerignore(c *check.C) {
	name := "test-with-utf8-bom-dockerignore"
	dockerfile := `
        FROM busybox
		ADD . /tmp/
		RUN ls -la /tmp
		RUN sh -c "! ls /tmp/Dockerfile"
		RUN ls /tmp/.dockerignore`
	dockerignore := []byte("./Dockerfile\n")
	bomDockerignore := append([]byte{0xEF, 0xBB, 0xBF}, dockerignore...)
	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", dockerfile),
		withFile(".dockerignore", string(bomDockerignore)),
	))
}

// #22489 Shell test to confirm config gets updated correctly
func (s *DockerSuite) TestBuildShellUpdatesConfig(c *check.C) {
	name := "testbuildshellupdatesconfig"

	buildImageSuccessfully(c, name, withDockerfile(`FROM `+minimalBaseImage()+`
        SHELL ["foo", "-bar"]`))
	expected := `["foo","-bar","#(nop) ","SHELL [foo -bar]"]`
	res := inspectFieldJSON(c, name, "ContainerConfig.Cmd")
	if res != expected {
		c.Fatalf("%s, expected %s", res, expected)
	}
	res = inspectFieldJSON(c, name, "ContainerConfig.Shell")
	if res != `["foo","-bar"]` {
		c.Fatalf(`%s, expected ["foo","-bar"]`, res)
	}
}

// #22489 Changing the shell multiple times and CMD after.
func (s *DockerSuite) TestBuildShellMultiple(c *check.C) {
	name := "testbuildshellmultiple"

	result := buildImage(name, withDockerfile(`FROM busybox
		RUN echo defaultshell
		SHELL ["echo"]
		RUN echoshell
		SHELL ["ls"]
		RUN -l
		CMD -l`))
	result.Assert(c, icmd.Success)

	// Must contain 'defaultshell' twice
	if len(strings.Split(result.Combined(), "defaultshell")) != 3 {
		c.Fatalf("defaultshell should have appeared twice in %s", result.Combined())
	}

	// Must contain 'echoshell' twice
	if len(strings.Split(result.Combined(), "echoshell")) != 3 {
		c.Fatalf("echoshell should have appeared twice in %s", result.Combined())
	}

	// Must contain "total " (part of ls -l)
	if !strings.Contains(result.Combined(), "total ") {
		c.Fatalf("%s should have contained 'total '", result.Combined())
	}

	// A container started from the image uses the shell-form CMD.
	// Last shell is ls. CMD is -l. So should contain 'total '.
	outrun, _ := dockerCmd(c, "run", "--rm", name)
	if !strings.Contains(outrun, "total ") {
		c.Fatalf("Expected started container to run ls -l. %s", outrun)
	}
}

// #22489. Changed SHELL with ENTRYPOINT
func (s *DockerSuite) TestBuildShellEntrypoint(c *check.C) {
	name := "testbuildshellentrypoint"

	buildImageSuccessfully(c, name, withDockerfile(`FROM busybox
		SHELL ["ls"]
		ENTRYPOINT -l`))
	// A container started from the image uses the shell-form ENTRYPOINT.
	// Shell is ls. ENTRYPOINT is -l. So should contain 'total '.
	outrun, _ := dockerCmd(c, "run", "--rm", name)
	if !strings.Contains(outrun, "total ") {
		c.Fatalf("Expected started container to run ls -l. %s", outrun)
	}
}

// #22489 Shell test to confirm shell is inherited in a subsequent build
func (s *DockerSuite) TestBuildShellInherited(c *check.C) {
	name1 := "testbuildshellinherited1"
	buildImageSuccessfully(c, name1, withDockerfile(`FROM busybox
        SHELL ["ls"]`))
	name2 := "testbuildshellinherited2"
	buildImage(name2, withDockerfile(`FROM `+name1+`
        RUN -l`)).Assert(c, icmd.Expected{
		// ls -l has "total " followed by some number in it, ls without -l does not.
		Out: "total ",
	})
}

// #22489 Shell test to confirm non-JSON doesn't work
func (s *DockerSuite) TestBuildShellNotJSON(c *check.C) {
	name := "testbuildshellnotjson"

	buildImage(name, withDockerfile(`FROM `+minimalBaseImage()+`
        sHeLl exec -form`, // Casing explicit to ensure error is upper-cased.
	)).Assert(c, icmd.Expected{
		ExitCode: 1,
		Err:      "SHELL requires the arguments to be in JSON form",
	})
}

// #22489 Windows shell test to confirm native is powershell if executing a PS command
// This would error if the default shell were still cmd.
func (s *DockerSuite) TestBuildShellWindowsPowershell(c *check.C) {
	testRequires(c, DaemonIsWindows)
	name := "testbuildshellpowershell"
	buildImage(name, withDockerfile(`FROM `+minimalBaseImage()+`
        SHELL ["powershell", "-command"]
		RUN Write-Host John`)).Assert(c, icmd.Expected{
		Out: "\nJohn\n",
	})
}

// Verify that escape is being correctly applied to words when escape directive is not \.
// Tests WORKDIR, ADD
func (s *DockerSuite) TestBuildEscapeNotBackslashWordTest(c *check.C) {
	testRequires(c, DaemonIsWindows)
	name := "testbuildescapenotbackslashwordtesta"
	buildImage(name, withDockerfile(`# escape= `+"`"+`
		FROM `+minimalBaseImage()+`
        WORKDIR c:\windows
		RUN dir /w`)).Assert(c, icmd.Expected{
		Out: "[System32]",
	})

	name = "testbuildescapenotbackslashwordtestb"
	buildImage(name, withDockerfile(`# escape= `+"`"+`
		FROM `+minimalBaseImage()+`
		SHELL ["powershell.exe"]
        WORKDIR c:\foo
		ADD Dockerfile c:\foo\
		RUN dir Dockerfile`)).Assert(c, icmd.Expected{
		Out: "-a----",
	})
}

// #22868. Make sure shell-form CMD is marked as escaped in the config of the image
func (s *DockerSuite) TestBuildCmdShellArgsEscaped(c *check.C) {
	testRequires(c, DaemonIsWindows)
	name := "testbuildcmdshellescaped"
	buildImageSuccessfully(c, name, withDockerfile(`
  FROM `+minimalBaseImage()+`
  CMD "ipconfig"
  `))
	res := inspectFieldJSON(c, name, "Config.ArgsEscaped")
	if res != "true" {
		c.Fatalf("CMD did not update Config.ArgsEscaped on image: %v", res)
	}
	dockerCmd(c, "run", "--name", "inspectme", name)
	dockerCmd(c, "wait", "inspectme")
	res = inspectFieldJSON(c, name, "Config.Cmd")

	if res != `["cmd","/S","/C","\"ipconfig\""]` {
		c.Fatalf("CMD was not escaped Config.Cmd: got %v", res)
	}
}

// Test case for #24912.
func (s *DockerSuite) TestBuildStepsWithProgress(c *check.C) {
	name := "testbuildstepswithprogress"
	totalRun := 5
	result := buildImage(name, withDockerfile("FROM busybox\n"+strings.Repeat("RUN echo foo\n", totalRun)))
	result.Assert(c, icmd.Success)
	c.Assert(result.Combined(), checker.Contains, fmt.Sprintf("Step 1/%d : FROM busybox", 1+totalRun))
	for i := 2; i <= 1+totalRun; i++ {
		c.Assert(result.Combined(), checker.Contains, fmt.Sprintf("Step %d/%d : RUN echo foo", i, 1+totalRun))
	}
}

func (s *DockerSuite) TestBuildWithFailure(c *check.C) {
	name := "testbuildwithfailure"

	// First test case can only detect `nobody` in runtime so all steps will show up
	dockerfile := "FROM busybox\nRUN nobody"
	result := buildImage(name, withDockerfile(dockerfile))
	c.Assert(result.Error, checker.NotNil)
	c.Assert(result.Stdout(), checker.Contains, "Step 1/2 : FROM busybox")
	c.Assert(result.Stdout(), checker.Contains, "Step 2/2 : RUN nobody")

	// Second test case `FFOM` should have been detected before build runs so no steps
	dockerfile = "FFOM nobody\nRUN nobody"
	result = buildImage(name, withDockerfile(dockerfile))
	c.Assert(result.Error, checker.NotNil)
	c.Assert(result.Stdout(), checker.Not(checker.Contains), "Step 1/2 : FROM busybox")
	c.Assert(result.Stdout(), checker.Not(checker.Contains), "Step 2/2 : RUN nobody")
}

func (s *DockerSuite) TestBuildCacheFromEqualDiffIDsLength(c *check.C) {
	dockerfile := `
		FROM busybox
		RUN echo "test"
		ENTRYPOINT ["sh"]`
	ctx := fakeContext(c, dockerfile, map[string]string{
		"Dockerfile": dockerfile,
	})
	defer ctx.Close()

	buildImageSuccessfully(c, "build1", withExternalBuildContext(ctx))
	id1 := getIDByName(c, "build1")

	// rebuild with cache-from
	result := buildImage("build2", withBuildFlags("--cache-from=build1"), withExternalBuildContext(ctx))
	result.Assert(c, icmd.Success)
	id2 := getIDByName(c, "build2")
	c.Assert(id1, checker.Equals, id2)
	c.Assert(strings.Count(result.Combined(), "Using cache"), checker.Equals, 2)
}

func (s *DockerSuite) TestBuildCacheFrom(c *check.C) {
	testRequires(c, DaemonIsLinux) // All tests that do save are skipped in windows
	dockerfile := `
		FROM busybox
		ENV FOO=bar
		ADD baz /
		RUN touch bax`
	ctx := fakeContext(c, dockerfile, map[string]string{
		"Dockerfile": dockerfile,
		"baz":        "baz",
	})
	defer ctx.Close()

	buildImageSuccessfully(c, "build1", withExternalBuildContext(ctx))
	id1 := getIDByName(c, "build1")

	// rebuild with cache-from
	result := buildImage("build2", withBuildFlags("--cache-from=build1"), withExternalBuildContext(ctx))
	result.Assert(c, icmd.Success)
	id2 := getIDByName(c, "build2")
	c.Assert(id1, checker.Equals, id2)
	c.Assert(strings.Count(result.Combined(), "Using cache"), checker.Equals, 3)
	dockerCmd(c, "rmi", "build2")

	// no cache match with unknown source
	result = buildImage("build2", withBuildFlags("--cache-from=nosuchtag"), withExternalBuildContext(ctx))
	result.Assert(c, icmd.Success)
	id2 = getIDByName(c, "build2")
	c.Assert(id1, checker.Not(checker.Equals), id2)
	c.Assert(strings.Count(result.Combined(), "Using cache"), checker.Equals, 0)
	dockerCmd(c, "rmi", "build2")

	// clear parent images
	tempDir, err := ioutil.TempDir("", "test-build-cache-from-")
	if err != nil {
		c.Fatalf("failed to create temporary directory: %s", tempDir)
	}
	defer os.RemoveAll(tempDir)
	tempFile := filepath.Join(tempDir, "img.tar")
	dockerCmd(c, "save", "-o", tempFile, "build1")
	dockerCmd(c, "rmi", "build1")
	dockerCmd(c, "load", "-i", tempFile)
	parentID, _ := dockerCmd(c, "inspect", "-f", "{{.Parent}}", "build1")
	c.Assert(strings.TrimSpace(parentID), checker.Equals, "")

	// cache still applies without parents
	result = buildImage("build2", withBuildFlags("--cache-from=build1"), withExternalBuildContext(ctx))
	result.Assert(c, icmd.Success)
	id2 = getIDByName(c, "build2")
	c.Assert(id1, checker.Equals, id2)
	c.Assert(strings.Count(result.Combined(), "Using cache"), checker.Equals, 3)
	history1, _ := dockerCmd(c, "history", "-q", "build2")

	// Retry, no new intermediate images
	result = buildImage("build3", withBuildFlags("--cache-from=build1"), withExternalBuildContext(ctx))
	result.Assert(c, icmd.Success)
	id3 := getIDByName(c, "build3")
	c.Assert(id1, checker.Equals, id3)
	c.Assert(strings.Count(result.Combined(), "Using cache"), checker.Equals, 3)
	history2, _ := dockerCmd(c, "history", "-q", "build3")

	c.Assert(history1, checker.Equals, history2)
	dockerCmd(c, "rmi", "build2")
	dockerCmd(c, "rmi", "build3")
	dockerCmd(c, "rmi", "build1")
	dockerCmd(c, "load", "-i", tempFile)

	// Modify file, everything up to last command and layers are reused
	dockerfile = `
		FROM busybox
		ENV FOO=bar
		ADD baz /
		RUN touch newfile`
	err = ioutil.WriteFile(filepath.Join(ctx.Dir, "Dockerfile"), []byte(dockerfile), 0644)
	c.Assert(err, checker.IsNil)

	result = buildImage("build2", withBuildFlags("--cache-from=build1"), withExternalBuildContext(ctx))
	result.Assert(c, icmd.Success)
	id2 = getIDByName(c, "build2")
	c.Assert(id1, checker.Not(checker.Equals), id2)
	c.Assert(strings.Count(result.Combined(), "Using cache"), checker.Equals, 2)

	layers1Str, _ := dockerCmd(c, "inspect", "-f", "{{json .RootFS.Layers}}", "build1")
	layers2Str, _ := dockerCmd(c, "inspect", "-f", "{{json .RootFS.Layers}}", "build2")

	var layers1 []string
	var layers2 []string
	c.Assert(json.Unmarshal([]byte(layers1Str), &layers1), checker.IsNil)
	c.Assert(json.Unmarshal([]byte(layers2Str), &layers2), checker.IsNil)

	c.Assert(len(layers1), checker.Equals, len(layers2))
	for i := 0; i < len(layers1)-1; i++ {
		c.Assert(layers1[i], checker.Equals, layers2[i])
	}
	c.Assert(layers1[len(layers1)-1], checker.Not(checker.Equals), layers2[len(layers1)-1])
}

func (s *DockerSuite) TestBuildNetNone(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildnetnone"
	buildImage(name, withBuildFlags("--network=none"), withDockerfile(`
  FROM busybox
  RUN ping -c 1 8.8.8.8
  `)).Assert(c, icmd.Expected{
		ExitCode: 1,
		Out:      "unreachable",
	})
}

func (s *DockerSuite) TestBuildNetContainer(c *check.C) {
	testRequires(c, DaemonIsLinux)

	id, _ := dockerCmd(c, "run", "--hostname", "foobar", "-d", "busybox", "nc", "-ll", "-p", "1234", "-e", "hostname")

	name := "testbuildnetcontainer"
	buildImageSuccessfully(c, name, withBuildFlags("--network=container:"+strings.TrimSpace(id)),
		withDockerfile(`
  FROM busybox
  RUN nc localhost 1234 > /otherhost
  `))

	host, _ := dockerCmd(c, "run", "testbuildnetcontainer", "cat", "/otherhost")
	c.Assert(strings.TrimSpace(host), check.Equals, "foobar")
}

func (s *DockerSuite) TestBuildWithExtraHost(c *check.C) {
	testRequires(c, DaemonIsLinux)

	name := "testbuildwithextrahost"
	buildImageSuccessfully(c, name,
		withBuildFlags(
			"--add-host", "foo:127.0.0.1",
			"--add-host", "bar:127.0.0.1",
		),
		withDockerfile(`
  FROM busybox
  RUN ping -c 1 foo
  RUN ping -c 1 bar
  `))
}

func (s *DockerSuite) TestBuildWithExtraHostInvalidFormat(c *check.C) {
	testRequires(c, DaemonIsLinux)
	dockerfile := `
		FROM busybox
		RUN ping -c 1 foo`

	testCases := []struct {
		testName   string
		dockerfile string
		buildFlag  string
	}{
		{"extra_host_missing_ip", dockerfile, "--add-host=foo"},
		{"extra_host_missing_ip_with_delimeter", dockerfile, "--add-host=foo:"},
		{"extra_host_missing_hostname", dockerfile, "--add-host=:127.0.0.1"},
		{"extra_host_invalid_ipv4", dockerfile, "--add-host=foo:101.10.2"},
		{"extra_host_invalid_ipv6", dockerfile, "--add-host=foo:2001::1::3F"},
	}

	for _, tc := range testCases {
		result := buildImage(tc.testName, withBuildFlags(tc.buildFlag), withDockerfile(tc.dockerfile))
		result.Assert(c, icmd.Expected{
			ExitCode: 125,
		})
	}

}

func (s *DockerSuite) TestBuildSquashParent(c *check.C) {
	testRequires(c, ExperimentalDaemon)
	dockerFile := `
		FROM busybox
		RUN echo hello > /hello
		RUN echo world >> /hello
		RUN echo hello > /remove_me
		ENV HELLO world
		RUN rm /remove_me
		`
	// build and get the ID that we can use later for history comparison
	name := "test"
	buildImageSuccessfully(c, name, withDockerfile(dockerFile))
	origID := getIDByName(c, name)

	// build with squash
	buildImageSuccessfully(c, name, withBuildFlags("--squash"), withDockerfile(dockerFile))
	id := getIDByName(c, name)

	out, _ := dockerCmd(c, "run", "--rm", id, "/bin/sh", "-c", "cat /hello")
	c.Assert(strings.TrimSpace(out), checker.Equals, "hello\nworld")

	dockerCmd(c, "run", "--rm", id, "/bin/sh", "-c", "[ ! -f /remove_me ]")
	dockerCmd(c, "run", "--rm", id, "/bin/sh", "-c", `[ "$(echo $HELLO)" == "world" ]`)

	// make sure the ID produced is the ID of the tag we specified
	inspectID := inspectImage(c, "test", ".ID")
	c.Assert(inspectID, checker.Equals, id)

	origHistory, _ := dockerCmd(c, "history", origID)
	testHistory, _ := dockerCmd(c, "history", "test")

	splitOrigHistory := strings.Split(strings.TrimSpace(origHistory), "\n")
	splitTestHistory := strings.Split(strings.TrimSpace(testHistory), "\n")
	c.Assert(len(splitTestHistory), checker.Equals, len(splitOrigHistory)+1)

	out = inspectImage(c, id, "len .RootFS.Layers")
	c.Assert(strings.TrimSpace(out), checker.Equals, "3")
}

func (s *DockerSuite) TestBuildContChar(c *check.C) {
	name := "testbuildcontchar"

	buildImage(name, withDockerfile(`FROM busybox\`)).Assert(c, icmd.Expected{
		Out: "Step 1/1 : FROM busybox",
	})

	result := buildImage(name, withDockerfile(`FROM busybox
		 RUN echo hi \`))
	result.Assert(c, icmd.Success)
	c.Assert(result.Combined(), checker.Contains, "Step 1/2 : FROM busybox")
	c.Assert(result.Combined(), checker.Contains, "Step 2/2 : RUN echo hi\n")

	result = buildImage(name, withDockerfile(`FROM busybox
		 RUN echo hi \\`))
	result.Assert(c, icmd.Success)
	c.Assert(result.Combined(), checker.Contains, "Step 1/2 : FROM busybox")
	c.Assert(result.Combined(), checker.Contains, "Step 2/2 : RUN echo hi \\\n")

	result = buildImage(name, withDockerfile(`FROM busybox
		 RUN echo hi \\\`))
	result.Assert(c, icmd.Success)
	c.Assert(result.Combined(), checker.Contains, "Step 1/2 : FROM busybox")
	c.Assert(result.Combined(), checker.Contains, "Step 2/2 : RUN echo hi \\\\\n")
}

// TestBuildOpaqueDirectory tests that a build succeeds which
// creates opaque directories.
// See https://github.com/docker/docker/issues/25244
func (s *DockerSuite) TestBuildOpaqueDirectory(c *check.C) {
	testRequires(c, DaemonIsLinux)
	dockerFile := `
		FROM busybox
		RUN mkdir /dir1 && touch /dir1/f1
		RUN rm -rf /dir1 && mkdir /dir1 && touch /dir1/f2
		RUN touch /dir1/f3
		RUN [ -f /dir1/f2 ]
		`
	// Test that build succeeds, last command fails if opaque directory
	// was not handled correctly
	buildImageSuccessfully(c, "testopaquedirectory", withDockerfile(dockerFile))
}

// Windows test for USER in dockerfile
func (s *DockerSuite) TestBuildWindowsUser(c *check.C) {
	testRequires(c, DaemonIsWindows)
	name := "testbuildwindowsuser"
	buildImage(name, withDockerfile(`FROM `+testEnv.MinimalBaseImage()+`
		RUN net user user /add
		USER user
		RUN set username
		`)).Assert(c, icmd.Expected{
		Out: "USERNAME=user",
	})
}

// Verifies if COPY file . when WORKDIR is set to a non-existing directory,
// the directory is created and the file is copied into the directory,
// as opposed to the file being copied as a file with the name of the
// directory. Fix for 27545 (found on Windows, but regression good for Linux too).
// Note 27545 was reverted in 28505, but a new fix was added subsequently in 28514.
func (s *DockerSuite) TestBuildCopyFileDotWithWorkdir(c *check.C) {
	name := "testbuildcopyfiledotwithworkdir"
	buildImageSuccessfully(c, name, withBuildContext(c,
		withFile("Dockerfile", `FROM busybox
WORKDIR /foo
COPY file .
RUN ["cat", "/foo/file"]
`),
		withFile("file", "content"),
	))
}

// Case-insensitive environment variables on Windows
func (s *DockerSuite) TestBuildWindowsEnvCaseInsensitive(c *check.C) {
	testRequires(c, DaemonIsWindows)
	name := "testbuildwindowsenvcaseinsensitive"
	buildImageSuccessfully(c, name, withDockerfile(`
		FROM `+testEnv.MinimalBaseImage()+`
		ENV FOO=bar foo=baz
  `))
	res := inspectFieldJSON(c, name, "Config.Env")
	if res != `["foo=baz"]` { // Should not have FOO=bar in it - takes the last one processed. And only one entry as deduped.
		c.Fatalf("Case insensitive environment variables on Windows failed. Got %s", res)
	}
}

// Test case for 29667
func (s *DockerSuite) TestBuildWorkdirImageCmd(c *check.C) {
	image := "testworkdirimagecmd"
	buildImageSuccessfully(c, image, withDockerfile(`
FROM busybox
WORKDIR /foo/bar
`))
	out, _ := dockerCmd(c, "inspect", "--format", "{{ json .Config.Cmd }}", image)

	// The Windows busybox image has a blank `cmd`
	lookingFor := `["sh"]`
	if testEnv.DaemonPlatform() == "windows" {
		lookingFor = "null"
	}
	c.Assert(strings.TrimSpace(out), checker.Equals, lookingFor)

	image = "testworkdirlabelimagecmd"
	buildImageSuccessfully(c, image, withDockerfile(`
FROM busybox
WORKDIR /foo/bar
LABEL a=b
`))

	out, _ = dockerCmd(c, "inspect", "--format", "{{ json .Config.Cmd }}", image)
	c.Assert(strings.TrimSpace(out), checker.Equals, lookingFor)
}

// Test case for 28902/28909
func (s *DockerSuite) TestBuildWorkdirCmd(c *check.C) {
	testRequires(c, DaemonIsLinux)
	name := "testbuildworkdircmd"
	dockerFile := `
                FROM busybox
                WORKDIR /
                `
	buildImageSuccessfully(c, name, withDockerfile(dockerFile))
	result := buildImage(name, withDockerfile(dockerFile))
	result.Assert(c, icmd.Success)
	c.Assert(strings.Count(result.Combined(), "Using cache"), checker.Equals, 1)
}

// FIXME(vdemeester) should be a unit test
func (s *DockerSuite) TestBuildLineErrorOnBuild(c *check.C) {
	name := "test_build_line_error_onbuild"
	buildImage(name, withDockerfile(`FROM busybox
  ONBUILD
  `)).Assert(c, icmd.Expected{
		ExitCode: 1,
		Err:      "Dockerfile parse error line 2: ONBUILD requires at least one argument",
	})
}

// FIXME(vdemeester) should be a unit test
func (s *DockerSuite) TestBuildLineErrorUknownInstruction(c *check.C) {
	name := "test_build_line_error_unknown_instruction"
	buildImage(name, withDockerfile(`FROM busybox
  RUN echo hello world
  NOINSTRUCTION echo ba
  RUN echo hello
  ERROR
  `)).Assert(c, icmd.Expected{
		ExitCode: 1,
		Err:      "Dockerfile parse error line 3: Unknown instruction: NOINSTRUCTION",
	})
}

// FIXME(vdemeester) should be a unit test
func (s *DockerSuite) TestBuildLineErrorWithEmptyLines(c *check.C) {
	name := "test_build_line_error_with_empty_lines"
	buildImage(name, withDockerfile(`
  FROM busybox

  RUN echo hello world

  NOINSTRUCTION echo ba

  CMD ["/bin/init"]
  `)).Assert(c, icmd.Expected{
		ExitCode: 1,
		Err:      "Dockerfile parse error line 6: Unknown instruction: NOINSTRUCTION",
	})
}

// FIXME(vdemeester) should be a unit test
func (s *DockerSuite) TestBuildLineErrorWithComments(c *check.C) {
	name := "test_build_line_error_with_comments"
	buildImage(name, withDockerfile(`FROM busybox
  # This will print hello world
  # and then ba
  RUN echo hello world
  NOINSTRUCTION echo ba
  `)).Assert(c, icmd.Expected{
		ExitCode: 1,
		Err:      "Dockerfile parse error line 5: Unknown instruction: NOINSTRUCTION",
	})
}
