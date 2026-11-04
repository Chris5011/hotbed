package cli

// `hotbedd image …` — custom images (issue #1, engine/images*.go).

import (
	"fmt"
	"os"
	"strings"

	"hotbed/internal/engine"
)

const imageUsage = `usage:
  hotbedd image ls                                    container + VM images
  hotbedd image build NAME --from STACK [--apt PKG]... [--run CMD]... [--env K=V]...
                                    declarative image hotbed-custom:NAME (STACK: go|node|
                                    python|rust|base|custom:<image>); use as stack custom:NAME
  hotbedd image rebuild NAME                          rebuild from the stored spec
  hotbedd image save SANDBOX NAME                     container -> hotbed-custom:NAME
                                    (secrets scrubbed, flattened; the sandbox is untouched)
  hotbedd image save-vm VM NAME                       shut-off VM -> VM base image NAME
                                    (generalized; hotbedvm create X --image NAME)
  hotbedd image rm NAME [--vm]                        delete (refused while in use)`

func imageCmd(args []string) error {
	verb := firstArg(args)
	rest := []string{}
	if len(args) > 1 {
		rest = args[1:]
	}
	logf := func(s string) { fmt.Printf("\033[36;1mhotbedd:\033[0m %s\n", s) }
	switch verb {
	case "ls", "list":
		return imageList()
	case "build":
		spec, err := parseImageSpec(rest)
		if err != nil {
			return err
		}
		if err := engine.BuildCustomImage(spec, os.Stdout); err != nil {
			return err
		}
		ok("built %s - use it as stack custom:%s\n", engine.CustomImageRef(spec.Name), spec.Name)
		return nil
	case "rebuild":
		spec, err := engine.LoadImageSpec(firstArg(rest))
		if err != nil {
			return fmt.Errorf("no stored spec for %q (only `image build` images can be rebuilt): %w", firstArg(rest), err)
		}
		if err := engine.BuildCustomImage(spec, os.Stdout); err != nil {
			return err
		}
		ok("rebuilt %s\n", engine.CustomImageRef(spec.Name))
		return nil
	case "save", "save-vm":
		if len(rest) < 2 {
			return fmt.Errorf("%s", imageUsage)
		}
		var scrubbed []string
		var err error
		if verb == "save" {
			scrubbed, err = engine.SaveContainerImage(rest[0], rest[1], logf)
		} else {
			scrubbed, err = engine.SaveVMImage(rest[0], rest[1], logf)
		}
		for _, f := range scrubbed {
			fmt.Printf("  scrubbed %s\n", f)
		}
		if err != nil {
			return err
		}
		if verb == "save" {
			ok("saved %s - use it as stack custom:%s\n", engine.CustomImageRef(rest[1]), rest[1])
		} else {
			ok("saved VM image %s - hotbedvm create NAME --image %s (or the wizard)\n", rest[1], rest[1])
		}
		return nil
	case "rm", "remove":
		n := firstArg(rest)
		if n == "" {
			return fmt.Errorf("%s", imageUsage)
		}
		var err error
		if hasFlag(rest, "--vm") {
			err = engine.RemoveVMImage(n)
		} else {
			err = engine.RemoveCustomImage(n)
		}
		if err != nil {
			return err
		}
		ok("removed %s\n", n)
		return nil
	}
	return fmt.Errorf("%s", imageUsage)
}

func parseImageSpec(a []string) (engine.ImageSpec, error) {
	s := engine.ImageSpec{Env: map[string]string{}}
	for i := 0; i < len(a); i++ {
		val := func() (string, error) {
			if i+1 >= len(a) {
				return "", fmt.Errorf("%s needs a value", a[i])
			}
			i++
			return a[i], nil
		}
		switch a[i] {
		case "--from", "--apt", "--run", "--env", "--description":
			flag := a[i]
			v, err := val()
			if err != nil {
				return s, err
			}
			switch flag {
			case "--from":
				s.From = v
			case "--apt":
				s.Apt = append(s.Apt, strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })...)
			case "--run":
				s.Run = append(s.Run, v)
			case "--env":
				k, x, ok := strings.Cut(v, "=")
				if !ok {
					return s, fmt.Errorf("--env %q: want K=V", v)
				}
				s.Env[k] = x
			case "--description":
				s.Description = v
			}
		default:
			if strings.HasPrefix(a[i], "-") || s.Name != "" {
				return s, fmt.Errorf("unexpected %q\n%s", a[i], imageUsage)
			}
			s.Name = a[i]
		}
	}
	if s.From == "" {
		s.From = "base"
	}
	return s, s.Validate()
}

func imageList() error {
	cs, err := engine.ListCustomImages()
	if err != nil {
		return err
	}
	fmt.Printf("%-24s %-9s %-10s %-10s %s\n", "CONTAINER IMAGE", "ORIGIN", "BASE", "SIZE", "CREATED")
	for _, c := range cs {
		fmt.Printf("%-24s %-9s %-10s %-10s %s\n", "custom:"+c.Name, c.Origin, c.Base, c.Size, c.Created)
	}
	if len(cs) == 0 {
		fmt.Println("(none - hotbedd image build / image save)")
	}
	fmt.Printf("\n%-24s %-20s %-8s %-8s %s\n", "VM IMAGE", "FROM", "BACKEND", "SIZE", "CREATED")
	vs := engine.ListVMImages()
	for _, v := range vs {
		fmt.Printf("%-24s %-20s %-8s %-8s %s\n", v.Name, v.From, v.Backend, fmt.Sprintf("%dM", v.SizeMB), v.Created)
	}
	if len(vs) == 0 {
		fmt.Println("(none - hotbedd image save-vm)")
	}
	return nil
}
