package query

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mithrandie/csvq/lib/cmd"
	"github.com/mithrandie/csvq/lib/file"
	"github.com/mithrandie/csvq/lib/parser"
	"github.com/mithrandie/csvq/lib/syntax"
	"github.com/mithrandie/csvq/lib/value"

	"github.com/mithrandie/go-text"
	"github.com/mithrandie/go-text/color"
	"github.com/mithrandie/go-text/fixedlen"
	"github.com/mithrandie/ternary"
)

type ObjectStatus int

const (
	ObjectFixed ObjectStatus = iota
	ObjectCreated
	ObjectUpdated
)

const IgnoredFlagPrefix = "(ignored) "

const (
	ReloadConfig = "CONFIG"
)

const (
	ShowTables    = "TABLES"
	ShowViews     = "VIEWS"
	ShowCursors   = "CURSORS"
	ShowFunctions = "FUNCTIONS"
	ShowFlags     = "FLAGS"
	ShowEnv       = "ENV"
	ShowRuninfo   = "RUNINFO"
)

var ShowObjectList = []string{
	ShowTables,
	ShowViews,
	ShowCursors,
	ShowFunctions,
	ShowFlags,
	ShowEnv,
	ShowRuninfo,
}

func Echo(expr parser.Echo, filter *Filter) (string, error) {
	p, err := filter.Evaluate(expr.Value)
	if err != nil {
		return "", err
	}

	return Formatter.Format("%s", []value.Primary{p})
}

func Print(expr parser.Print, filter *Filter) (string, error) {
	p, err := filter.Evaluate(expr.Value)
	if err != nil {
		return "", err
	}
	return p.String(), err
}

func Printf(expr parser.Printf, filter *Filter) (string, error) {
	var format string
	formatValue, err := filter.Evaluate(expr.Format)
	if err != nil {
		return "", err
	}
	formatString := value.ToString(formatValue)
	if !value.IsNull(formatString) {
		format = formatString.(value.String).Raw()
	}

	args := make([]value.Primary, len(expr.Values))
	for i, v := range expr.Values {
		p, err := filter.Evaluate(v)
		if err != nil {
			return "", err
		}
		args[i] = p
	}

	message, err := Formatter.Format(format, args)
	if err != nil {
		return "", NewReplaceValueLengthError(expr, err.(AppError).ErrorMessage())
	}
	return message, nil
}

func Source(expr parser.Source, filter *Filter) ([]parser.Statement, error) {
	var fpath string

	if ident, ok := expr.FilePath.(parser.Identifier); ok {
		fpath = ident.Literal
	} else {
		p, err := filter.Evaluate(expr.FilePath)
		if err != nil {
			return nil, err
		}
		s := value.ToString(p)
		if value.IsNull(s) {
			return nil, NewSourceInvalidFilePathError(expr, expr.FilePath)
		}
		fpath = s.(value.String).Raw()
	}

	if len(fpath) < 1 {
		return nil, NewSourceInvalidFilePathError(expr, expr.FilePath)
	}

	return LoadStatementsFromFile(expr, fpath)
}

func LoadStatementsFromFile(expr parser.Source, fpath string) ([]parser.Statement, error) {
	if !filepath.IsAbs(fpath) {
		if abs, err := filepath.Abs(fpath); err == nil {
			fpath = abs
		}
	}

	if !file.Exists(fpath) {
		return nil, NewSourceFileNotExistError(expr, fpath)
	}

	h, err := file.NewHandlerForRead(fpath)
	if err != nil {
		return nil, NewReadFileError(expr, err.Error())
	}
	defer h.Close()

	buf, err := ioutil.ReadAll(h.FileForRead())
	if err != nil {
		return nil, NewReadFileError(expr, err.Error())
	}
	input := string(buf)

	statements, err := parser.Parse(input, fpath)
	if err != nil {
		err = NewSyntaxError(err.(*parser.SyntaxError))
	}
	return statements, err
}

func ParseExecuteStatements(expr parser.Execute, filter *Filter) ([]parser.Statement, error) {
	var input string
	stmt, err := filter.Evaluate(expr.Statements)
	if err != nil {
		return nil, err
	}
	stmt = value.ToString(stmt)
	if !value.IsNull(stmt) {
		input = stmt.(value.String).Raw()
	}

	args := make([]value.Primary, len(expr.Values))
	for i, v := range expr.Values {
		p, err := filter.Evaluate(v)
		if err != nil {
			return nil, err
		}
		args[i] = p
	}

	input, err = Formatter.Format(input, args)
	if err != nil {
		return nil, NewReplaceValueLengthError(expr, err.(AppError).ErrorMessage())
	}
	statements, err := parser.Parse(input, fmt.Sprintf("(L:%d C:%d) EXECUTE", expr.Line(), expr.Char()))
	if err != nil {
		err = NewSyntaxError(err.(*parser.SyntaxError))
	}
	return statements, err
}

func SetFlag(expr parser.SetFlag, filter *Filter) error {
	var p value.Primary
	var err error

	if ident, ok := expr.Value.(parser.Identifier); ok {
		p = value.NewString(ident.Literal)
	} else {
		p, err = filter.Evaluate(expr.Value)
		if err != nil {
			return err
		}
	}

	switch strings.ToUpper(expr.Name) {
	case cmd.RepositoryFlag, cmd.TimezoneFlag, cmd.DatetimeFormatFlag, cmd.DelimiterFlag, cmd.JsonQueryFlag, cmd.EncodingFlag,
		cmd.WriteEncodingFlag, cmd.FormatFlag, cmd.WriteDelimiterFlag, cmd.LineBreakFlag, cmd.JsonEscape:
		p = value.ToString(p)
	case cmd.NoHeaderFlag, cmd.WithoutNullFlag, cmd.WithoutHeaderFlag, cmd.EncloseAll, cmd.PrettyPrintFlag,
		cmd.EastAsianEncodingFlag, cmd.CountDiacriticalSignFlag, cmd.CountFormatCodeFlag, cmd.ColorFlag, cmd.QuietFlag, cmd.StatsFlag:
		p = value.ToBoolean(p)
	case cmd.WaitTimeoutFlag:
		p = value.ToFloat(p)
	case cmd.CPUFlag:
		p = value.ToInteger(p)
	default:
		return NewInvalidFlagNameError(expr, expr.Name)
	}
	if value.IsNull(p) {
		return NewFlagValueNotAllowedFormatError(expr)
	}

	flags := cmd.GetFlags()

	switch strings.ToUpper(expr.Name) {
	case cmd.RepositoryFlag:
		err = flags.SetRepository(p.(value.String).Raw())
	case cmd.TimezoneFlag:
		err = flags.SetLocation(p.(value.String).Raw())
	case cmd.DatetimeFormatFlag:
		flags.SetDatetimeFormat(p.(value.String).Raw())
	case cmd.WaitTimeoutFlag:
		flags.SetWaitTimeout(p.(value.Float).Raw())
	case cmd.DelimiterFlag:
		err = flags.SetDelimiter(p.(value.String).Raw())
	case cmd.JsonQueryFlag:
		flags.SetJsonQuery(p.(value.String).Raw())
	case cmd.EncodingFlag:
		err = flags.SetEncoding(p.(value.String).Raw())
	case cmd.NoHeaderFlag:
		flags.SetNoHeader(p.(value.Boolean).Raw())
	case cmd.WithoutNullFlag:
		flags.SetWithoutNull(p.(value.Boolean).Raw())
	case cmd.FormatFlag:
		err = flags.SetFormat(p.(value.String).Raw(), "")
	case cmd.WriteEncodingFlag:
		err = flags.SetWriteEncoding(p.(value.String).Raw())
	case cmd.WriteDelimiterFlag:
		err = flags.SetWriteDelimiter(p.(value.String).Raw())
	case cmd.WithoutHeaderFlag:
		flags.SetWithoutHeader(p.(value.Boolean).Raw())
	case cmd.LineBreakFlag:
		err = flags.SetLineBreak(p.(value.String).Raw())
	case cmd.EncloseAll:
		flags.SetEncloseAll(p.(value.Boolean).Raw())
	case cmd.JsonEscape:
		err = flags.SetJsonEscape(p.(value.String).Raw())
	case cmd.PrettyPrintFlag:
		flags.SetPrettyPrint(p.(value.Boolean).Raw())
	case cmd.EastAsianEncodingFlag:
		flags.SetEastAsianEncoding(p.(value.Boolean).Raw())
	case cmd.CountDiacriticalSignFlag:
		flags.SetCountDiacriticalSign(p.(value.Boolean).Raw())
	case cmd.CountFormatCodeFlag:
		flags.SetCountFormatCode(p.(value.Boolean).Raw())
	case cmd.ColorFlag:
		flags.SetColor(p.(value.Boolean).Raw())
	case cmd.QuietFlag:
		flags.SetQuiet(p.(value.Boolean).Raw())
	case cmd.CPUFlag:
		flags.SetCPU(int(p.(value.Integer).Raw()))
	case cmd.StatsFlag:
		flags.SetStats(p.(value.Boolean).Raw())
	}

	if err != nil {
		return NewInvalidFlagValueError(expr, err.Error())
	}
	return nil
}

func AddFlagElement(expr parser.AddFlagElement, filter *Filter) error {
	switch strings.ToUpper(expr.Name) {
	case cmd.DatetimeFormatFlag:
		e := parser.SetFlag{
			BaseExpr: expr.GetBaseExpr(),
			Name:     expr.Name,
			Value:    expr.Value,
		}
		return SetFlag(e, filter)
	case cmd.RepositoryFlag, cmd.TimezoneFlag, cmd.DelimiterFlag, cmd.JsonQueryFlag, cmd.EncodingFlag,
		cmd.WriteEncodingFlag, cmd.FormatFlag, cmd.WriteDelimiterFlag, cmd.LineBreakFlag, cmd.JsonEscape,
		cmd.NoHeaderFlag, cmd.WithoutNullFlag, cmd.WithoutHeaderFlag, cmd.EncloseAll, cmd.PrettyPrintFlag,
		cmd.EastAsianEncodingFlag, cmd.CountDiacriticalSignFlag, cmd.CountFormatCodeFlag, cmd.ColorFlag, cmd.QuietFlag, cmd.StatsFlag,
		cmd.WaitTimeoutFlag,
		cmd.CPUFlag:

		return NewAddFlagNotSupportedNameError(expr)
	default:
		return NewInvalidFlagNameError(expr, expr.Name)
	}
}

func RemoveFlagElement(expr parser.RemoveFlagElement, filter *Filter) error {
	var p value.Primary
	var err error

	p, err = filter.Evaluate(expr.Value)
	if err != nil {
		return err
	}

	switch strings.ToUpper(expr.Name) {
	case cmd.DatetimeFormatFlag:
		flags := cmd.GetFlags()

		if i := value.ToInteger(p); !value.IsNull(i) {
			idx := int(i.(value.Integer).Raw())
			if -1 < idx && idx < len(flags.DatetimeFormat) {
				flags.DatetimeFormat = append(flags.DatetimeFormat[:idx], flags.DatetimeFormat[idx+1:]...)
			}

		} else if s := value.ToString(p); !value.IsNull(s) {
			val := s.(value.String).Raw()
			formats := make([]string, 0, len(flags.DatetimeFormat))
			for _, v := range flags.DatetimeFormat {
				if val != v {
					formats = append(formats, v)
				}
			}
			flags.DatetimeFormat = formats
		} else {
			return NewInvalidFlagValueToBeRemovedError(expr)
		}
	case cmd.RepositoryFlag, cmd.TimezoneFlag, cmd.DelimiterFlag, cmd.JsonQueryFlag, cmd.EncodingFlag,
		cmd.WriteEncodingFlag, cmd.FormatFlag, cmd.WriteDelimiterFlag, cmd.LineBreakFlag, cmd.JsonEscape,
		cmd.NoHeaderFlag, cmd.WithoutNullFlag, cmd.WithoutHeaderFlag, cmd.EncloseAll, cmd.PrettyPrintFlag,
		cmd.EastAsianEncodingFlag, cmd.CountDiacriticalSignFlag, cmd.CountFormatCodeFlag, cmd.ColorFlag, cmd.QuietFlag, cmd.StatsFlag,
		cmd.WaitTimeoutFlag,
		cmd.CPUFlag:

		return NewRemoveFlagNotSupportedNameError(expr)
	default:
		return NewInvalidFlagNameError(expr, expr.Name)
	}

	return nil
}

func ShowFlag(expr parser.ShowFlag) (string, error) {
	s, err := showFlag(expr.Name)
	if err != nil {
		return s, NewInvalidFlagNameError(expr, expr.Name)
	}

	palette, _ := cmd.GetPalette()
	return palette.Render(cmd.LableEffect, cmd.FlagSymbol(strings.ToUpper(expr.Name)+":")) + " " + s, nil
}

func showFlag(flag string) (string, error) {
	var s string

	flags := cmd.GetFlags()
	palette, _ := cmd.GetPalette()

	switch strings.ToUpper(flag) {
	case cmd.RepositoryFlag:
		if len(flags.Repository) < 1 {
			wd, _ := os.Getwd()
			s = palette.Render(cmd.NullEffect, fmt.Sprintf("(current dir: %s)", wd))
		} else {
			s = palette.Render(cmd.StringEffect, flags.Repository)
		}
	case cmd.TimezoneFlag:
		s = palette.Render(cmd.StringEffect, flags.Location)
	case cmd.DatetimeFormatFlag:
		if len(flags.DatetimeFormat) < 1 {
			s = palette.Render(cmd.NullEffect, "(not set)")
		} else {
			list := make([]string, 0, len(flags.DatetimeFormat))
			for _, f := range flags.DatetimeFormat {
				list = append(list, "\""+f+"\"")
			}
			s = palette.Render(cmd.StringEffect, "["+strings.Join(list, ", ")+"]")
		}
	case cmd.WaitTimeoutFlag:
		s = palette.Render(cmd.NumberEffect, value.Float64ToStr(flags.WaitTimeout))
	case cmd.DelimiterFlag:
		d := "'" + cmd.EscapeString(string(flags.Delimiter)) + "'"
		p := fixedlen.DelimiterPositions(flags.DelimiterPositions).String()

		switch flags.SelectImportFormat() {
		case cmd.CSV, cmd.TSV:
			s = palette.Render(cmd.StringEffect, d) + palette.Render(cmd.LableEffect, " | ") + palette.Render(cmd.NullEffect, p)
		case cmd.FIXED:
			s = palette.Render(cmd.NullEffect, d) + palette.Render(cmd.LableEffect, " | ") + palette.Render(cmd.StringEffect, p)
		default:
			s = palette.Render(cmd.NullEffect, IgnoredFlagPrefix+d+" | "+p)
		}
	case cmd.JsonQueryFlag:
		q := flags.JsonQuery
		if len(q) < 1 {
			q = "(empty)"
		}

		switch flags.SelectImportFormat() {
		case cmd.JSON:
			s = palette.Render(cmd.StringEffect, q)
		default:
			s = palette.Render(cmd.NullEffect, IgnoredFlagPrefix+q)
		}
	case cmd.EncodingFlag:
		s = palette.Render(cmd.StringEffect, flags.Encoding.String())
	case cmd.NoHeaderFlag:
		s = palette.Render(cmd.BooleanEffect, strconv.FormatBool(flags.NoHeader))
	case cmd.WithoutNullFlag:
		s = palette.Render(cmd.BooleanEffect, strconv.FormatBool(flags.WithoutNull))
	case cmd.FormatFlag:
		s = palette.Render(cmd.StringEffect, flags.Format.String())
	case cmd.WriteEncodingFlag:
		switch flags.Format {
		case cmd.JSON:
			s = palette.Render(cmd.NullEffect, IgnoredFlagPrefix+flags.WriteEncoding.String())
		default:
			s = palette.Render(cmd.StringEffect, flags.WriteEncoding.String())
		}
	case cmd.WriteDelimiterFlag:
		d := "'" + cmd.EscapeString(string(flags.WriteDelimiter)) + "'"
		p := fixedlen.DelimiterPositions(flags.WriteDelimiterPositions).String()
		switch flags.Format {
		case cmd.CSV:
			s = palette.Render(cmd.StringEffect, d) + palette.Render(cmd.LableEffect, " | ") + palette.Render(cmd.NullEffect, p)
		case cmd.FIXED:
			s = palette.Render(cmd.NullEffect, d) + palette.Render(cmd.LableEffect, " | ") + palette.Render(cmd.StringEffect, p)
		default:
			s = palette.Render(cmd.NullEffect, IgnoredFlagPrefix+d+" | "+p)
		}
	case cmd.WithoutHeaderFlag:
		s = strconv.FormatBool(flags.WithoutHeader)
		switch flags.Format {
		case cmd.CSV, cmd.TSV, cmd.FIXED, cmd.GFM, cmd.ORG:
			s = palette.Render(cmd.BooleanEffect, s)
		default:
			s = palette.Render(cmd.NullEffect, IgnoredFlagPrefix+s)
		}
	case cmd.LineBreakFlag:
		s = palette.Render(cmd.StringEffect, flags.LineBreak.String())
	case cmd.EncloseAll:
		s = strconv.FormatBool(flags.EncloseAll)
		switch flags.Format {
		case cmd.CSV, cmd.TSV:
			s = palette.Render(cmd.BooleanEffect, s)
		default:
			s = palette.Render(cmd.NullEffect, IgnoredFlagPrefix+s)
		}
	case cmd.JsonEscape:
		s = cmd.JsonEscapeTypeToString(flags.JsonEscape)
		switch flags.Format {
		case cmd.JSON:
			s = palette.Render(cmd.StringEffect, s)
		default:
			s = palette.Render(cmd.NullEffect, IgnoredFlagPrefix+s)
		}
	case cmd.PrettyPrintFlag:
		s = strconv.FormatBool(flags.PrettyPrint)
		switch flags.Format {
		case cmd.JSON:
			s = palette.Render(cmd.BooleanEffect, s)
		default:
			s = palette.Render(cmd.NullEffect, IgnoredFlagPrefix+s)
		}
	case cmd.EastAsianEncodingFlag:
		s = strconv.FormatBool(flags.EastAsianEncoding)
		switch flags.Format {
		case cmd.GFM, cmd.ORG, cmd.TEXT:
			s = palette.Render(cmd.BooleanEffect, s)
		default:
			s = palette.Render(cmd.NullEffect, IgnoredFlagPrefix+s)
		}
	case cmd.CountDiacriticalSignFlag:
		s = strconv.FormatBool(flags.CountDiacriticalSign)
		switch flags.Format {
		case cmd.GFM, cmd.ORG, cmd.TEXT:
			s = palette.Render(cmd.BooleanEffect, s)
		default:
			s = palette.Render(cmd.NullEffect, IgnoredFlagPrefix+s)
		}
	case cmd.CountFormatCodeFlag:
		s = strconv.FormatBool(flags.CountFormatCode)
		switch flags.Format {
		case cmd.GFM, cmd.ORG, cmd.TEXT:
			s = palette.Render(cmd.BooleanEffect, s)
		default:
			s = palette.Render(cmd.NullEffect, IgnoredFlagPrefix+s)
		}
	case cmd.ColorFlag:
		s = palette.Render(cmd.BooleanEffect, strconv.FormatBool(flags.Color))
	case cmd.QuietFlag:
		s = palette.Render(cmd.BooleanEffect, strconv.FormatBool(flags.Quiet))
	case cmd.CPUFlag:
		s = palette.Render(cmd.NumberEffect, strconv.Itoa(flags.CPU))
	case cmd.StatsFlag:
		s = palette.Render(cmd.BooleanEffect, strconv.FormatBool(flags.Stats))
	default:
		return s, errors.New("invalid flag name")
	}

	return s, nil
}

func ShowObjects(expr parser.ShowObjects, filter *Filter) (string, error) {
	var s string

	w := NewObjectWriter()

	switch strings.ToUpper(expr.Type.Literal) {
	case ShowTables:
		keys := ViewCache.SortedKeys()

		if len(keys) < 1 {
			s = cmd.Warn("No table is loaded")
		} else {
			createdFiles, updatedFiles := UncommittedViews.UncommittedFiles()

			for _, key := range keys {
				fields := ViewCache[key].Header.TableColumnNames()
				info := ViewCache[key].FileInfo
				ufpath := strings.ToUpper(info.Path)

				if _, ok := createdFiles[ufpath]; ok {
					w.WriteColor("*Created* ", cmd.EmphasisEffect)
				} else if _, ok := updatedFiles[ufpath]; ok {
					w.WriteColor("*Updated* ", cmd.EmphasisEffect)
				}
				w.WriteColorWithoutLineBreak(info.Path, cmd.ObjectEffect)
				writeFields(w, fields)

				w.NewLine()
				writeTableAttribute(w, info)
				w.ClearBlock()
				w.NewLine()
			}

			uncommitted := len(createdFiles) + len(updatedFiles)

			w.Title1 = "Loaded Tables"
			if 0 < uncommitted {
				w.Title2 = fmt.Sprintf("(Uncommitted: %s)", FormatCount(uncommitted, "Table"))
				w.Title2Effect = cmd.EmphasisEffect
			}
			s = "\n" + w.String() + "\n"
		}
	case ShowViews:
		views := filter.TempViews.All()

		if len(views) < 1 {
			s = cmd.Warn("No view is declared")
		} else {
			keys := views.SortedKeys()

			updatedViews := UncommittedViews.UncommittedTempViews()

			for _, key := range keys {
				fields := views[key].Header.TableColumnNames()
				info := views[key].FileInfo
				ufpath := strings.ToUpper(info.Path)

				if _, ok := updatedViews[ufpath]; ok {
					w.WriteColor("*Updated* ", cmd.EmphasisEffect)
				}
				w.WriteColorWithoutLineBreak(info.Path, cmd.ObjectEffect)
				writeFields(w, fields)
				w.ClearBlock()
				w.NewLine()
			}

			uncommitted := len(updatedViews)

			w.Title1 = "Views"
			if 0 < uncommitted {
				w.Title2 = fmt.Sprintf("(Uncommitted: %s)", FormatCount(uncommitted, "View"))
				w.Title2Effect = cmd.EmphasisEffect
			}
			s = "\n" + w.String() + "\n"
		}
	case ShowCursors:
		cursors := filter.Cursors.All()
		if len(cursors) < 1 {
			s = cmd.Warn("No cursor is declared")
		} else {
			keys := cursors.SortedKeys()

			for _, key := range keys {
				cur := cursors[key]
				isOpen := cur.IsOpen()

				w.WriteColor(cur.name, cmd.ObjectEffect)
				w.BeginBlock()

				w.NewLine()
				w.WriteColorWithoutLineBreak("Status: ", cmd.LableEffect)
				if isOpen == ternary.TRUE {
					nor, _ := cur.Count()
					inRange, _ := cur.IsInRange()
					position, _ := cur.Pointer()

					norStr := cmd.FormatInt(nor, ",")

					w.WriteColorWithoutLineBreak("Open", cmd.TernaryEffect)
					w.WriteColorWithoutLineBreak("    Number of Rows: ", cmd.LableEffect)
					w.WriteColorWithoutLineBreak(norStr, cmd.NumberEffect)
					w.WriteSpaces(10 - len(norStr))
					w.WriteColorWithoutLineBreak("Pointer: ", cmd.LableEffect)
					switch inRange {
					case ternary.FALSE:
						w.WriteColorWithoutLineBreak("Out of Range", cmd.TernaryEffect)
					case ternary.UNKNOWN:
						w.WriteColorWithoutLineBreak(inRange.String(), cmd.TernaryEffect)
					default:
						w.WriteColorWithoutLineBreak(cmd.FormatInt(position, ","), cmd.NumberEffect)
					}
				} else {
					w.WriteColorWithoutLineBreak("Closed", cmd.TernaryEffect)
				}

				w.NewLine()
				w.WriteColorWithoutLineBreak("Query: ", cmd.LableEffect)
				w.WriteColorWithoutLineBreak(cur.query.String(), cmd.IdentifierEffect)

				w.ClearBlock()
				w.NewLine()
			}
			w.Title1 = "Cursors"
			s = "\n" + w.String() + "\n"
		}
	case ShowFunctions:
		scalas, aggs := filter.Functions.All()
		if len(scalas) < 1 && len(aggs) < 1 {
			s = cmd.Warn("No function is declared")
		} else {
			if 0 < len(scalas) {
				w.Clear()
				writeFunctions(w, scalas)
				w.Title1 = "Scala Functions"
				s += "\n" + w.String()
			}
			if 0 < len(aggs) {
				w.Clear()
				writeFunctions(w, aggs)
				w.Title1 = "Aggregate Functions"
				s += "\n" + w.String() + "\n"
			} else {
				s += "\n"
			}
		}
	case ShowFlags:
		for _, flag := range cmd.FlagList {
			symbol := cmd.FlagSymbol(flag)
			s, _ := showFlag(flag)
			w.WriteSpaces(24 - len(symbol))
			w.WriteColorWithoutLineBreak(symbol, cmd.LableEffect)
			w.WriteColorWithoutLineBreak(":", cmd.LableEffect)
			w.WriteSpaces(1)
			w.WriteWithoutLineBreak(s)
			w.NewLine()
		}
		w.Title1 = "Flags"
		s = "\n" + w.String() + "\n"
	case ShowEnv:
		env := os.Environ()
		names := make([]string, 0, len(env))
		vars := make([]string, 0, len(env))
		nameWidth := 0

		for _, e := range env {
			words := strings.Split(e, "=")
			name := string(parser.VariableSign) + string(parser.EnvironmentVariableSign) + words[0]
			if nameWidth < len(name) {
				nameWidth = len(name)
			}

			var val string
			if 1 < len(words) {
				val = strings.Join(words[1:], "=")
			}
			vars = append(vars, val)
			names = append(names, name)
		}

		for i, name := range names {
			w.WriteSpaces(nameWidth - len(name))
			w.WriteColorWithoutLineBreak(name, cmd.LableEffect)
			w.WriteColorWithoutLineBreak(":", cmd.LableEffect)
			w.WriteSpaces(1)
			w.WriteWithoutLineBreak(vars[i])
			w.NewLine()
		}
		w.Title1 = "Environment Variables"
		s = "\n" + w.String() + "\n"
	case ShowRuninfo:
		for _, ri := range RuntimeInformatinList {
			label := string(parser.VariableSign) + string(parser.RuntimeInformationSign) + ri
			p, _ := GetRuntimeInformation(parser.RuntimeInformation{Name: ri})

			w.WriteSpaces(19 - len(label))
			w.WriteColorWithoutLineBreak(label, cmd.LableEffect)
			w.WriteColorWithoutLineBreak(":", cmd.LableEffect)
			w.WriteSpaces(1)
			switch ri {
			case WorkingDirectory, VersionInformation:
				w.WriteColorWithoutLineBreak(p.(value.String).Raw(), cmd.StringEffect)
			case UncommittedInformation:
				w.WriteColorWithoutLineBreak(p.(value.Boolean).String(), cmd.BooleanEffect)
			default:
				w.WriteColorWithoutLineBreak(p.(value.Integer).String(), cmd.NumberEffect)
			}
			w.NewLine()
		}
		w.Title1 = "Runtime Information"
		s = "\n" + w.String() + "\n"
	default:
		return "", NewShowInvalidObjectTypeError(expr, expr.Type.String())
	}

	return s, nil
}

func writeTableAttribute(w *ObjectWriter, info *FileInfo) {
	w.WriteColor("Format: ", cmd.LableEffect)
	w.WriteWithoutLineBreak(info.Format.String())

	w.WriteSpaces(8 - cmd.TextWidth(info.Format.String()))
	switch info.Format {
	case cmd.CSV:
		w.WriteColorWithoutLineBreak("Delimiter: ", cmd.LableEffect)
		w.WriteWithoutLineBreak("'" + cmd.EscapeString(string(info.Delimiter)) + "'")
	case cmd.TSV:
		w.WriteColorWithoutLineBreak("Delimiter: ", cmd.LableEffect)
		w.WriteColorWithoutLineBreak("'\\t'", cmd.NullEffect)
	case cmd.FIXED:
		w.WriteColorWithoutLineBreak("Delimiter Positions: ", cmd.LableEffect)
		w.WriteWithoutLineBreak(info.DelimiterPositions.String())
	case cmd.JSON:
		escapeStr := cmd.JsonEscapeTypeToString(info.JsonEscape)
		w.WriteColorWithoutLineBreak("Escape: ", cmd.LableEffect)
		w.WriteWithoutLineBreak(escapeStr)

		spaces := 9 - len(escapeStr)
		if spaces < 2 {
			spaces = 2
		}
		w.WriteSpaces(spaces)

		w.WriteColorWithoutLineBreak("Query: ", cmd.LableEffect)
		if len(info.JsonQuery) < 1 {
			w.WriteColorWithoutLineBreak("(empty)", cmd.NullEffect)
		} else {
			w.WriteColorWithoutLineBreak(info.JsonQuery, cmd.NullEffect)
		}
	}

	switch info.Format {
	case cmd.CSV, cmd.TSV:
		w.WriteSpaces(4 - (cmd.TextWidth(cmd.EscapeString(string(info.Delimiter)))))
		w.WriteColorWithoutLineBreak("Enclose All: ", cmd.LableEffect)
		w.WriteWithoutLineBreak(strconv.FormatBool(info.EncloseAll))
	}

	w.NewLine()

	w.WriteColor("Encoding: ", cmd.LableEffect)
	switch info.Format {
	case cmd.JSON:
		w.WriteColorWithoutLineBreak(text.UTF8.String(), cmd.NullEffect)
	default:
		w.WriteWithoutLineBreak(info.Encoding.String())
	}

	w.WriteSpaces(6 - (cmd.TextWidth(info.Encoding.String())))
	w.WriteColorWithoutLineBreak("LineBreak: ", cmd.LableEffect)
	w.WriteWithoutLineBreak(info.LineBreak.String())

	switch info.Format {
	case cmd.JSON:
		w.WriteSpaces(6 - (cmd.TextWidth(info.LineBreak.String())))
		w.WriteColorWithoutLineBreak("Pretty Print: ", cmd.LableEffect)
		w.WriteWithoutLineBreak(strconv.FormatBool(info.PrettyPrint))
	case cmd.CSV, cmd.TSV, cmd.FIXED, cmd.GFM, cmd.ORG:
		w.WriteSpaces(6 - (cmd.TextWidth(info.LineBreak.String())))
		w.WriteColorWithoutLineBreak("Header: ", cmd.LableEffect)
		w.WriteWithoutLineBreak(strconv.FormatBool(!info.NoHeader))
	}
}

func writeFields(w *ObjectWriter, fields []string) {
	w.BeginBlock()
	w.NewLine()
	w.WriteColor("Fields: ", cmd.LableEffect)
	w.BeginSubBlock()
	lastIdx := len(fields) - 1
	for i, f := range fields {
		escaped := cmd.EscapeString(f)
		if i < lastIdx && !w.FitInLine(escaped+", ") {
			w.NewLine()
		}
		w.WriteColor(escaped, cmd.AttributeEffect)
		if i < lastIdx {
			w.WriteWithoutLineBreak(", ")
		}
	}
	w.EndSubBlock()
}

func writeFunctions(w *ObjectWriter, funcs UserDefinedFunctionMap) {
	keys := funcs.SortedKeys()

	for _, key := range keys {
		fn := funcs[key]

		w.WriteColor(fn.Name.String(), cmd.ObjectEffect)
		w.WriteWithoutLineBreak(" (")

		if fn.IsAggregate {
			w.WriteColorWithoutLineBreak(fn.Cursor.String(), cmd.IdentifierEffect)
			if 0 < len(fn.Parameters) {
				w.WriteWithoutLineBreak(", ")
			}
		}

		for i, p := range fn.Parameters {
			if 0 < i {
				w.WriteWithoutLineBreak(", ")
			}
			if def, ok := fn.Defaults[p.Name]; ok {
				w.WriteColorWithoutLineBreak(p.String(), cmd.AttributeEffect)
				w.WriteWithoutLineBreak(" = ")
				w.WriteColorWithoutLineBreak(def.String(), cmd.ValueEffect)
			} else {
				w.WriteColorWithoutLineBreak(p.String(), cmd.AttributeEffect)
			}
		}

		w.WriteWithoutLineBreak(")")
		w.ClearBlock()
		w.NewLine()
	}
}

func ShowFields(expr parser.ShowFields, filter *Filter) (string, error) {
	if !strings.EqualFold(expr.Type.Literal, "FIELDS") {
		return "", NewShowInvalidObjectTypeError(expr, expr.Type.Literal)
	}

	var status = ObjectFixed

	view := NewView()
	err := view.LoadFromTableIdentifier(expr.Table, filter.CreateNode())
	if err != nil {
		return "", err
	}

	if view.FileInfo.IsTemporary {
		updatedViews := UncommittedViews.UncommittedTempViews()
		ufpath := strings.ToUpper(view.FileInfo.Path)

		if _, ok := updatedViews[ufpath]; ok {
			status = ObjectUpdated
		}
	} else {
		createdViews, updatedView := UncommittedViews.UncommittedFiles()
		ufpath := strings.ToUpper(view.FileInfo.Path)

		if _, ok := createdViews[ufpath]; ok {
			status = ObjectCreated
		} else if _, ok := updatedView[ufpath]; ok {
			status = ObjectUpdated
		}
	}

	w := NewObjectWriter()
	w.WriteColorWithoutLineBreak("Type: ", cmd.LableEffect)
	if view.FileInfo.IsTemporary {
		w.WriteWithoutLineBreak("View")
	} else {
		w.WriteWithoutLineBreak("Table")
		w.NewLine()
		w.WriteColorWithoutLineBreak("Path: ", cmd.LableEffect)
		w.WriteColorWithoutLineBreak(view.FileInfo.Path, cmd.ObjectEffect)
		w.NewLine()
		writeTableAttribute(w, view.FileInfo)
	}

	w.NewLine()
	w.WriteColorWithoutLineBreak("Status: ", cmd.LableEffect)
	switch status {
	case ObjectCreated:
		w.WriteColorWithoutLineBreak("Created", cmd.EmphasisEffect)
	case ObjectUpdated:
		w.WriteColorWithoutLineBreak("Updated", cmd.EmphasisEffect)
	default:
		w.WriteWithoutLineBreak("Fixed")
	}

	w.NewLine()
	writeFieldList(w, view.Header.TableColumnNames())

	w.Title1 = "Fields in"
	w.Title2 = expr.Table.Literal
	w.Title2Effect = cmd.IdentifierEffect
	return "\n" + w.String() + "\n", nil
}

func writeFieldList(w *ObjectWriter, fields []string) {
	l := len(fields)
	digits := len(strconv.Itoa(l))
	fieldNumbers := make([]string, 0, l)
	for i := 0; i < l; i++ {
		idxstr := strconv.Itoa(i + 1)
		fieldNumbers = append(fieldNumbers, strings.Repeat(" ", digits-len(idxstr))+idxstr)
	}

	w.WriteColorWithoutLineBreak("Fields:", cmd.LableEffect)
	w.NewLine()
	w.WriteSpaces(2)
	w.BeginSubBlock()
	for i := 0; i < l; i++ {
		w.WriteColor(fieldNumbers[i], cmd.NumberEffect)
		w.Write(".")
		w.WriteSpaces(1)
		w.WriteColorWithoutLineBreak(fields[i], cmd.AttributeEffect)
		w.NewLine()
	}
}

func SetEnvVar(expr parser.SetEnvVar, filter *Filter) error {
	var p value.Primary
	var err error

	if ident, ok := expr.Value.(parser.Identifier); ok {
		p = value.NewString(ident.Literal)
	} else {
		p, err = filter.Evaluate(expr.Value)
		if err != nil {
			return err
		}
	}

	var val string
	if p = value.ToString(p); !value.IsNull(p) {
		val = p.(value.String).Raw()
	}
	return os.Setenv(expr.EnvVar.Name, val)
}

func UnsetEnvVar(expr parser.UnsetEnvVar) error {
	return os.Unsetenv(expr.EnvVar.Name)
}

func Chdir(expr parser.Chdir, filter *Filter) error {
	var dirpath string
	var err error

	if ident, ok := expr.DirPath.(parser.Identifier); ok {
		dirpath = ident.Literal
	} else {
		p, err := filter.Evaluate(expr.DirPath)
		if err != nil {
			return err
		}
		s := value.ToString(p)
		if value.IsNull(s) {
			return NewPathError(expr, expr.DirPath.String(), "invalid directory path")
		}
		dirpath = s.(value.String).Raw()
	}

	if err = os.Chdir(dirpath); err != nil {
		if patherr, ok := err.(*os.PathError); ok {
			err = NewPathError(expr, patherr.Path, patherr.Err.Error())
		}
	}
	return err
}

func Pwd(expr parser.Pwd) (string, error) {
	dirpath, err := os.Getwd()
	if err != nil {
		if patherr, ok := err.(*os.PathError); ok {
			err = NewPathError(expr, patherr.Path, patherr.Err.Error())
		}
	}
	return dirpath, err
}

func Reload(expr parser.Reload) error {
	switch strings.ToUpper(expr.Type.Literal) {
	case ReloadConfig:
		if err := cmd.LoadEnvironment(); err != nil {
			return NewLoadConfigurationError(expr, err.Error())
		}

		env, _ := cmd.GetEnvironment()

		flags := cmd.GetFlags()
		for _, v := range env.DatetimeFormat {
			flags.DatetimeFormat = cmd.AppendStrIfNotExist(flags.DatetimeFormat, v)
		}

		palette, err := color.GeneratePalette(env.Palette)
		if err != nil {
			return NewLoadConfigurationError(expr, err.Error())
		}
		oldPalette, _ := cmd.GetPalette()
		oldPalette.Merge(palette)

		if Terminal != nil {
			if err := Terminal.ReloadConfig(); err != nil {
				return NewLoadConfigurationError(expr, err.Error())
			}
		}

	default:
		return NewInvalidReloadTypeError(expr, expr.Type.Literal)
	}
	return nil
}

func Syntax(expr parser.Syntax, filter *Filter) string {
	keys := make([]string, 0, len(expr.Keywords))
	for _, key := range expr.Keywords {
		var keystr string
		if fr, ok := key.(parser.FieldReference); ok {
			keystr = fr.Column.Literal
		} else {
			if p, err := filter.Evaluate(key); err == nil {
				if s := value.ToString(p); !value.IsNull(s) {
					keystr = s.(value.String).Raw()
				}
			}
		}

		if 0 < len(keystr) {
			words := strings.Split(strings.TrimSpace(keystr), " ")
			for _, w := range words {
				w = strings.TrimSpace(w)
				if 0 < len(w) {
					keys = append(keys, w)
				}
			}
		}
	}

	store := syntax.NewStore()
	exps := store.Search(keys)

	var p *color.Palette
	if cmd.GetFlags().Color {
		p, _ = cmd.GetPalette()
	}

	w := NewObjectWriter()

	for _, exp := range exps {
		w.WriteColor(exp.Label, cmd.LableEffect)
		w.NewLine()
		if len(exps) < 4 {
			w.BeginBlock()

			if 0 < len(exp.Description.Template) {
				w.WriteWithAutoLineBreak(exp.Description.Format(p))
				w.NewLine()
				w.NewLine()
			}

			for _, def := range exp.Grammar {
				w.Write(def.Name.Format(p))
				w.NewLine()
				w.BeginBlock()
				for i, gram := range def.Group {
					if i == 0 {
						w.Write(": ")
					} else {
						w.Write("| ")
					}
					w.BeginSubBlock()
					w.WriteWithAutoLineBreak(gram.Format(p))
					w.EndSubBlock()
					w.NewLine()
				}

				if 0 < len(def.Description.Template) {
					if 0 < len(def.Group) {
						w.NewLine()
					}
					w.WriteWithAutoLineBreak(def.Description.Format(p))
					w.NewLine()
				}

				w.EndBlock()
				w.NewLine()
			}
			w.EndBlock()
		}

		if 0 < len(exp.Children) && (len(keys) < 1 || strings.EqualFold(exp.Label, strings.Join(keys, " "))) {
			w.BeginBlock()
			for _, child := range exp.Children {
				w.WriteColor(child.Label, cmd.LableEffect)
				w.NewLine()
			}
		}

		w.ClearBlock()
	}

	if len(keys) < 1 {
		w.Title1 = "Contents"
	} else {
		w.Title1 = "Search: " + strings.Join(keys, " ")
	}
	return "\n" + w.String() + "\n"

}
