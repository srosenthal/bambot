package main

import (
    "io/ioutil"
    "testing"
)

func TestMatchEdgeCases(t *testing.T) {
    assertNonMatch(t, "")
    assertNonMatch(t, "\n")
    assertNonMatch(t, "abc")
}

func TestMatchJavaCompilationError(t *testing.T) {
    start := "[ERROR] COMPILATION ERROR"
    end := "[INFO] ------------------------------------------------------------------------"
    bodyStr := start + "\n" + "bla bla bla" + "\n" + end
    assertMatch(t, bodyStr, "Bambot detected a Java compilation error!")
}

func TestCSharpError(t *testing.T) {
    fileName := "test_files/csharp-1.log"
    bodyStr := readFileToString(fileName)
    assertMatch(t, bodyStr, "Bambot detected a C# build error!")
}

func TestPythonError(t *testing.T) {
    fileName := "test_files/python-1.log"
    bodyStr := readFileToString(fileName)
    assertMatch(t, bodyStr, "Bambot detected a Python error!")
}

func assertNonMatch(t *testing.T, bodyStr string) ScanResult {
    scanResult := scanString(bodyStr)
    if scanResult != nonMatch() {
       t.Errorf("expected '%s' to not match any rules, result was '%s'", bodyStr, scanResult)
    }
    return scanResult
}

func assertMatch(t *testing.T, bodyStr string, expectedComment string) ScanResult {
    scanResult := scanString(bodyStr)
    if scanResult == nonMatch() {
        t.Errorf("expected '%s' to match a rule, but it matched nothing", truncate(bodyStr))
    }
    if scanResult.Comment != expectedComment {
        t.Errorf("expected comment '%s' but found '%s'", expectedComment, scanResult.Comment)
    }
    return scanResult
}

func truncate(bodyStr string) string {
    if len(bodyStr) > 50 {
        return bodyStr[0:50]
    } else {
        return bodyStr
    }
}

func readFileToString(fileName string) string {
    content, err := ioutil.ReadFile(fileName)
    if err != nil {
        panic(err)
    }
    return string(content)
}