package cli

import (
	"encoding/csv"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type FlagType string

const (
	FlagTypeDir  FlagType = "dir"
	FlagTypeFile FlagType = "file"
)

type AddFlagOptions struct {
	// This function must return a slice of regexps to be matched agains all environment variables.
	// Values of matched environment variables will become the flag value. Values priority (from
	// lowest to highest): flag default value -> environment variable value (from first to last
	// regexp; if regexp matches multiple env vars then the last has higher priority) -> cli flag
	// value. For slice and map-type flags: all env vars values and all cli flags values are joined.
	GetEnvVarRegexesFunc GetFlagEnvVarRegexesInterface

	// Group info is saved in Flag annotations, which can be used later, e.g. for grouping flags in
	// the --help output.
	Group *FlagGroup

	Type       FlagType
	ShortName  string
	Deprecated bool
	Hidden     bool
	Required   bool
}

// TODO(ilya-lesikov): allow restricted values
// TODO(ilya-lesikov): allow showing restricted values in usage
// TODO(ilya-lesikov): pass examples separately from help
// TODO(ilya-lesikov): allow for []string with no comma-separated values (pflag.StringArrayVar?)
// TODO(ilya-lesikov): allow for map[string]string with no comma-separated values

// Create and bind a flag to the Cobra command. Corresponding environment variables (if enabled)
// parsed and the value is assigned to the flag immediately. Flag value type inferred from
// destination arg.
func AddFlag[T any](cmd *cobra.Command, dest *T, name string, defaultValue T, help string, opts AddFlagOptions) error {
	opts, err := applyAddOptionsDefaults(opts, dest)
	if err != nil {
		return fmt.Errorf("apply defaults: %w", err)
	}

	envVarRegexExprs, err := opts.GetEnvVarRegexesFunc(cmd, name)
	if err != nil {
		return fmt.Errorf("get env var names: %w", err)
	}

	help, err = buildHelp(help, dest, envVarRegexExprs)
	if err != nil {
		return fmt.Errorf("build help: %w", err)
	}

	if err := addFlags(cmd, dest, name, opts.ShortName, defaultValue, help); err != nil {
		return fmt.Errorf("add flags: %w", err)
	}

	if opts.Hidden {
		if err := cmd.Flags().MarkHidden(name); err != nil {
			return fmt.Errorf("mark flag as hidden: %w", err)
		}
	}

	if opts.Deprecated {
		if err := cmd.Flags().MarkDeprecated(name, "remove it to hide this message."); err != nil {
			return fmt.Errorf("mark flag as deprecated: %w", err)
		}
	}

	if opts.Required {
		if err := cmd.MarkFlagRequired(name); err != nil {
			return fmt.Errorf("mark flag as required: %w", err)
		}
	}

	if err := processEnvVars(cmd, envVarRegexExprs, name, dest); err != nil {
		return fmt.Errorf("process env vars: %w", err)
	}

	switch opts.Type {
	case FlagTypeDir:
		if err := cmd.MarkFlagDirname(name); err != nil {
			return fmt.Errorf("mark flag as a directory: %w", err)
		}
	case FlagTypeFile:
		if err := cmd.MarkFlagFilename(name); err != nil {
			return fmt.Errorf("mark flag as a filename: %w", err)
		}
	}

	if opts.Group != nil {
		if err := saveFlagGroupMetadata(cmd, name, opts.Group); err != nil {
			return fmt.Errorf("save flag group metadata: %w", err)
		}
	}

	return nil
}

func applyAddOptionsDefaults[T any](opts AddFlagOptions, dest *T) (AddFlagOptions, error) {
	if opts.GetEnvVarRegexesFunc == nil {
		switch dst := any(dest).(type) {
		case *bool, *int, *string, *time.Duration:
			opts.GetEnvVarRegexesFunc = GetFlagLocalEnvVarRegexes
		case *[]string, *map[string]string:
			opts.GetEnvVarRegexesFunc = GetFlagLocalMultiEnvVarRegexes
		default:
			return AddFlagOptions{}, fmt.Errorf("unsupported type %T", dst)
		}
	}

	return opts, nil
}

func buildHelp[T any](help string, dest *T, envVarRegexes []*FlagRegexExpr) (string, error) {
	if !strings.HasSuffix(help, ".") {
		help += "."
	}

	if len(envVarRegexes) == 0 {
		return help, nil
	} else if len(envVarRegexes) == 1 {
		help = fmt.Sprintf("%s Var: %s", help, envVarRegexes[0].Human)
	} else {
		var envVarRegexesHuman []string
		for _, envVarRegex := range envVarRegexes {
			envVarRegexesHuman = append(envVarRegexesHuman, envVarRegex.Human)
		}

		help = fmt.Sprintf("%s Vars: %s", help, strings.Join(envVarRegexesHuman, ", "))
	}

	return help, nil
}

func addFlags[T any](cmd *cobra.Command, dest *T, name string, shortName string, defaultValue T, help string) error {
	switch dst := any(dest).(type) {
	case *bool:
		cmd.Flags().BoolVarP(dst, name, shortName, any(defaultValue).(bool), help)
	case *int:
		cmd.Flags().IntVarP(dst, name, shortName, any(defaultValue).(int), help)
	case *string:
		cmd.Flags().StringVarP(dst, name, shortName, any(defaultValue).(string), help)
	case *[]string:
		cmd.Flags().StringArrayVarP(dst, name, shortName, any(defaultValue).([]string), help)
	case *map[string]string:
		cmd.Flags().StringToStringVarP(dst, name, shortName, any(defaultValue).(map[string]string), help)
	case *time.Duration:
		cmd.Flags().DurationVarP(dst, name, shortName, any(defaultValue).(time.Duration), help)
	default:
		return fmt.Errorf("unsupported type %T", dst)
	}

	return nil
}

func processEnvVars[T any](cmd *cobra.Command, envVarRegexExprs []*FlagRegexExpr, flagName string, dest T) error {
	for _, regExpr := range envVarRegexExprs {
		regex, err := regexp.Compile(fmt.Sprintf(`%s`, regExpr.Expr))
		if err != nil {
			return fmt.Errorf("compile regex %q: %w", regExpr.Expr, err)
		}

		definedFlagEnvVarRegexes[*regExpr] = regex
	}

	lo.Reverse(envVarRegexExprs)

	environ := os.Environ()
	sort.Strings(environ)

	envir := map[string]string{}
	for _, keyValue := range environ {
		parts := strings.SplitN(keyValue, "=", 2)
		envir[parts[0]] = parts[1]
	}

	envs := map[string]string{}
	for key, val := range envir {
		for _, regexExpr := range envVarRegexExprs {
			if !definedFlagEnvVarRegexes[*regexExpr].MatchString(key) || val == "" {
				continue
			}

			envs[key] = val
		}
	}

	switch dst := any(dest).(type) {
	case *bool, *int, *string, *time.Duration:
	envirLoop:
		for key, val := range envir {
			for _, regexExpr := range envVarRegexExprs {
				if !definedFlagEnvVarRegexes[*regexExpr].MatchString(key) || val == "" {
					continue
				}

				flag := cmd.Flag(flagName)
				flag.Changed = true

				if err := flag.Value.Set(val); err != nil {
					return fmt.Errorf("environment variable %q value %q is not valid: %w", key, val, err)
				}

				break envirLoop
			}
		}
	case *[]string:
		for key, val := range envs {
			flag := cmd.Flag(flagName)
			flag.Changed = true

			// For StringArrayVar, we need to append each environment variable value
			// without splitting on commas, letting the Helm library handle the parsing
			if err := flag.Value.(pflag.SliceValue).Append(val); err != nil {
				return fmt.Errorf("environment variable %q value %q is not valid: %w", key, val, err)
			}
		}
	case *map[string]string:
		for key, val := range envs {
			flag := cmd.Flag(flagName)
			flag.Changed = true

			if err := flag.Value.Set(val); err != nil {
				return fmt.Errorf("environment variable %q value %q is not valid: %w", key, val, err)
			}
		}
	default:
		return fmt.Errorf("unsupported type %T", dst)
	}

	return nil
}

func saveFlagGroupMetadata(cmd *cobra.Command, flagName string, group *FlagGroup) error {
	if err := cmd.Flags().SetAnnotation(flagName, FlagGroupIDAnnotationName, []string{group.ID}); err != nil {
		return fmt.Errorf("set group id annotation: %w", err)
	}

	if err := cmd.Flags().SetAnnotation(flagName, FlagGroupTitleAnnotationName, []string{group.Title}); err != nil {
		return fmt.Errorf("set group title annotation: %w", err)
	}

	if err := cmd.Flags().SetAnnotation(flagName, FlagGroupPriorityAnnotationName, []string{fmt.Sprintf("%d", group.Priority)}); err != nil {
		return fmt.Errorf("set group priority annotation: %w", err)
	}

	return nil
}

func splitComma(s string) ([]string, error) {
	stringReader := strings.NewReader(s)
	csvReader := csv.NewReader(stringReader)

	parts, err := csvReader.Read()
	if err != nil {
		return nil, fmt.Errorf("read csv values: %w", err)
	}

	return parts, nil
}
