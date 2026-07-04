package office

func flatWorkbookMetadata(format, engine string) map[string]string {
	return map[string]string{
		"workbook_mode":          "flat_text",
		"workbook_format":        format,
		"workbook_parser_engine": engine,
		"table_fidelity":         "low",
	}
}

func degradedWorkbookMetadata(format, engine string) map[string]string {
	return map[string]string{
		"workbook_mode":          "degraded_text",
		"workbook_format":        format,
		"workbook_parser_engine": engine,
		"table_fidelity":         "low",
		"workbook_warning_count": "1",
	}
}

func mergeMetadata(base map[string]string, extra map[string]string) map[string]string {
	if len(base) == 0 {
		return extra
	}
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
