package nodepath

import "testing"

// Expected values are what Node.js's path.posix returns for the same input.
func TestPosixFunctions(t *testing.T) {
	cases := []struct {
		name, got, want string
	}{
		{"normalize empty", Normalize(""), "."},
		{"normalize trailing", Normalize("a//b/../c/"), "a/c/"},
		{"normalize above root", Normalize("../../x/.."), "../.."},
		{"normalize dot slash", Normalize("./"), "./"},
		{"normalize root", Normalize("/../a"), "/a"},
		{"join", Join("a", "", "../b", "c/"), "b/c/"},
		{"join empty", Join("", ""), "."},
		{"resolve", ok(Resolve("/a/b", "../c", "./d")), "/a/c/d"},
		{"resolve absolute wins", ok(Resolve("/x", "/y/z/")), "/y/z"},
		{"relative", ok(Relative("/a/b/c", "/a/d")), "../../d"},
		{"relative same", ok(Relative("/a/b", "/a/b/")), ""},
		{"relative root", ok(Relative("/", "/a/b")), "a/b"},
		{"relative prefix", ok(Relative("/a/bc", "/a/b")), "../b"},
		{"dirname", Dirname("/a/b/"), "/a"},
		{"dirname root", Dirname("//a"), "//"},
		{"dirname relative", Dirname("a"), "."},
		{"basename", Basename("/a/b.txt/", ""), "b.txt"},
		{"basename suffix", Basename("/a/b.txt", ".txt"), "b"},
		{"basename whole suffix", Basename("b.txt", "b.txt"), ""},
		{"extname", Extname("a/b.tar.gz"), ".gz"},
		{"extname dotfile", Extname(".bashrc"), ""},
		{"extname trailing dot", Extname("a."), "."},
		{"extname dots", Extname(".."), ""},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// ok returns the path of a call that cannot fail in these cases (every input is absolute).
func ok(p string, err error) string {
	if err != nil {
		panic(err)
	}
	return p
}
