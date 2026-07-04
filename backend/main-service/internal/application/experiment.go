// Experiment planner (P2.4): turn a verified/scored hypothesis into a concrete,
// actionable experiment plan. The hypothesis statement drives retrieval (so the
// corpus surfaces method/characterization sources), and the LLM is asked to emit
// a STRICT-JSON plan grounded in the statement + detail: materials/reagents,
// process parameters with ranges, characterization & test methods, controls,
// measurable success criteria, a coarse cost/time estimate and key risks. The
// plan is merged into detail.experiment_plan (other detail keys preserved) and a
// "planned" revision is appended.

package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
	dbv1 "github.com/example/main-service/internal/platform/genproto/db/v1"
	"github.com/example/main-service/internal/platform/jsonx"
)

const experimentEvidenceTop = 10

// actionPlanned marks the revision appended when an experiment plan is produced.
const actionPlanned = "planned"

// Canonical coarse time-scale labels for the experiment plan's effort estimate.
const (
	timeDays   = "days"
	timeWeeks  = "weeks"
	timeMonths = "months"
)

// Canonical experiment_type values shared by the planner's domain specs,
// normalizeExperimentType and the generation fallback.
const (
	expTypeNewAlloy         = "new_alloy"
	expTypeProcessRoute     = "process_route"
	expTypeCoatingCorrosion = "coating_corrosion"
	expTypeBatteryMaterial  = "battery_material"
	expTypeOreBeneficiation = "ore_beneficiation"
	expTypeMetallurgy       = "metallurgy_process"
	expTypeGeneric          = "generic"
)

// Causal-chain stage labels reused by the generation fallback and the
// experiment-type aliases.
const (
	stageProcess  = "процесс"
	stageProperty = "свойство"
)

// experimentPlan is the strict-JSON shape the LLM is asked to emit. costLevel is
// one of low/medium/high; timeScale is one of days/weeks/months — both coarse on
// purpose so the UI can badge effort without faking precision.
type experimentPlan struct {
	ExperimentType   string              `json:"experiment_type"`
	Materials        []string            `json:"materials"`
	Sections         []experimentSection `json:"sections"`
	ProcessParams    []experimentParam   `json:"process_parameters"`
	Characterization []string            `json:"characterization_methods"`
	TestMethods      []string            `json:"test_methods"`
	Controls         []string            `json:"controls"`
	SuccessCriteria  []string            `json:"success_criteria"`
	EstimatedCost    string              `json:"estimated_cost"`
	EstimatedTime    string              `json:"estimated_time"`
	Risks            []string            `json:"risks"`
}

// experimentSection is a domain-specific block of the plan. It lets the UI show
// a new-alloy plan differently from a coating, battery-material or process-route
// plan while preserving the generic fields above.
type experimentSection struct {
	Title   string   `json:"title"`
	Purpose string   `json:"purpose"`
	Items   []string `json:"items"`
}

// experimentParam is one controllable process parameter with its working range.
type experimentParam struct {
	Name  string `json:"name"`
	Range string `json:"range"`
}

type experimentDomainSpec struct {
	Type     string
	Label    string
	Guidance string
	Sections []experimentSectionSpec
}

type experimentSectionSpec struct {
	Title    string
	Purpose  string
	Guidance string
}

// PlanExperiment loads the owner's hypothesis, asks the LLM for a grounded
// experiment plan, stores it under detail.experiment_plan and returns the
// reloaded hypothesis. Parsing is defensive (same {…} extraction + backslash
// repair as verify/generate); a parse/RPC failure surfaces as an error so the
// click reports honestly rather than persisting an empty plan.
func (s *HypothesisService) PlanExperiment(ctx context.Context, ownerID, id string) (*commonv1.Hypothesis, error) {
	ctx, cancel := withDeadline(ctx, llmSinglePassTimeout)
	defer cancel()
	h, err := s.GetHypothesis(ctx, ownerID, false, id)
	if err != nil {
		return nil, err
	}
	resp, aerr := s.answerer.Answer(ctx, &commonv1.RagRequest{
		OwnerId: ownerID,
		Query:   h.GetStatement(),         // statement → retrieve method/characterization sources
		Prompt:  buildExperimentPrompt(h), // long instruction → LLM only
		TopK:    experimentEvidenceTop,
	})
	if aerr != nil {
		return nil, aerr
	}
	plan, perr := parseExperimentPlan(resp.GetAnswer())
	if perr != nil {
		return nil, perr
	}
	h.Detail = mergeExperimentPlan(h.GetDetail(), plan, resp.GetModel())
	rev := &commonv1.HypothesisRevision{
		HypothesisId: id, Action: actionPlanned, EditorId: editorSystem,
		Summary: experimentSummary(plan), Patch: "",
	}
	if uerr := s.cat.UpdateHypothesis(ctx, &dbv1.UpdateHypothesisRequest{Hypothesis: h, Revision: rev}); uerr != nil {
		return nil, uerr
	}
	return s.GetHypothesis(ctx, ownerID, false, id)
}

// buildExperimentPrompt asks for a concrete, measurable plan grounded in the
// hypothesis statement + detail and the retrieved context (methods, conditions).
func buildExperimentPrompt(h *commonv1.Hypothesis) string {
	var b strings.Builder
	spec := experimentDomainSpecForHypothesis(h)
	b.WriteString("Гипотеза: «")
	b.WriteString(h.GetStatement())
	b.WriteString("».")
	if d := compactText(h.GetDetail()); d != "" {
		b.WriteString("\nДанные гипотезы (паспорт, JSON): ")
		b.WriteString(d)
	}
	b.WriteString("\n\nТы — инженер-экспериментатор НИОКР. Опираясь на гипотезу и приведённый выше контекст " +
		"(выдержки из документов с методиками и условиями), составь КОНКРЕТНЫЙ план эксперимента, который " +
		"подтвердит или опровергнет гипотезу. Сначала выбери experiment_type: new_alloy, process_route, " +
		"coating_corrosion, battery_material, ore_beneficiation (обогащение руды: дробление/измельчение/" +
		"флотация/гравитация/магнитная сепарация), metallurgy_process (плавка/выщелачивание/электролиз/обжиг) " +
		"или generic. Добавь sections — предметные этапы именно для " +
		"этого типа исследования, а не универсальный чеклист. Укажи: материалы/реактивы; параметры процесса с диапазонами " +
		"значений; методы характеризации и испытаний; контроли/базовый образец для сравнения; ИЗМЕРИМЫЕ " +
		"критерии успеха; ориентировочную стоимость (low|medium|high); ориентировочный срок (days|weeks|months); " +
		"ключевые риски. Не выдумывай числа, которых нет в контексте, кроме явно предлагаемых диапазонов варьирования.")
	b.WriteString("\nРекомендуемая предметная специализация: experiment_type=")
	b.WriteString(spec.Type)
	b.WriteString(" (")
	b.WriteString(spec.Label)
	b.WriteString("). ")
	b.WriteString(spec.Guidance)
	b.WriteString("\nОжидаемые sections для этого домена:")
	for _, section := range spec.Sections {
		b.WriteString("\n- ")
		b.WriteString(section.Title)
		b.WriteString(": ")
		b.WriteString(section.Purpose)
		if section.Guidance != "" {
			b.WriteString(" ")
			b.WriteString(section.Guidance)
		}
	}
	b.WriteString(untrustedContextInstruction)
	b.WriteString(langInstruction)
	b.WriteString("\nВерни СТРОГО JSON без markdown:\n")
	b.WriteString(`{"experiment_type":"new_alloy|process_route|coating_corrosion|battery_material|` +
		`ore_beneficiation|metallurgy_process|generic",` +
		`"sections":[{"title":"этап","purpose":"зачем нужен этап","items":["конкретное действие"]}],` +
		`"materials":["материал/реактив"],` +
		`"process_parameters":[{"name":"параметр","range":"диапазон значений с единицами"}],` +
		`"characterization_methods":["SEM","XRD"],"test_methods":["tensile test"],` +
		`"controls":["базовый образец/контроль"],"success_criteria":["измеримый критерий"],` +
		`"estimated_cost":"low|medium|high","estimated_time":"days|weeks|months","risks":["ключевой риск"]}`)
	return b.String()
}

func experimentDomainSpecForHypothesis(h *commonv1.Hypothesis) experimentDomainSpec {
	return experimentDomainSpecForText(h.GetTitle() + " " + h.GetStatement() + " " + h.GetDetail())
}

func experimentDomainSpecForText(text string) experimentDomainSpec {
	text = strings.ToLower(text)
	switch {
	case containsAny(text, "катод", "анод", "battery", "cell", "ёмкост", "емкост"):
		return experimentDomainSpec{
			Type:     expTypeBatteryMaterial,
			Label:    "батарейный материал",
			Guidance: "План должен отделять синтез материала от изготовления электрода и электрохимической проверки.",
			Sections: batteryMaterialSections(),
		}
	case containsAny(text, "корроз", "coating", "покрыт", "защитн"):
		return experimentDomainSpec{
			Type:     expTypeCoatingCorrosion,
			Label:    "покрытие и коррозия",
			Guidance: "План должен явно разделять подготовку образца, нанесение покрытия и коррозионное испытание.",
			Sections: coatingCorrosionSections(),
		}
	case containsAny(text, "выщелач", "электролиз", "обжиг", "металлург"):
		return experimentDomainSpec{
			Type:     expTypeMetallurgy,
			Label:    "металлургический передел",
			Guidance: "План должен идти от шихтовки к режиму агрегата, контролю состава и балансу металла.",
			Sections: metallurgyProcessSections(),
		}
	case containsAny(text, "флотац", "обогащ", "измельчен", "гравитац", "магнитн", "концентрат", "хвост", "извлечен"):
		return experimentDomainSpec{
			Type:     expTypeOreBeneficiation,
			Label:    "обогащение руды",
			Guidance: "План должен идти от характеристики питания к схеме опыта, реагентному режиму и балансу металла.",
			Sections: oreBeneficiationSections(),
		}
	case containsAny(text, "сплав", "alloy", "плав", "лить"):
		return experimentDomainSpec{
			Type:     expTypeNewAlloy,
			Label:    "новый сплав",
			Guidance: "План должен идти от состава и шихты к плавке, термообработке, микроструктуре и испытаниям.",
			Sections: newAlloySections(),
		}
	case containsAny(text, "маршрут", "process", "режим", "сверл", "обработ", "бурен", "drill"):
		return experimentDomainSpec{
			Type:     expTypeProcessRoute,
			Label:    "технологический маршрут",
			Guidance: "План должен сравнивать новый маршрут с базовым и фиксировать окно режимов, качество и производительность.",
			Sections: processRouteSections(),
		}
	default:
		return experimentDomainSpec{
			Type:     expTypeGeneric,
			Label:    "общий НИОКР-план",
			Guidance: "Если домен не очевиден, sections всё равно должны вести от объекта и воздействия к измерению свойства и KPI.",
			Sections: genericSections(),
		}
	}
}

func batteryMaterialSections() []experimentSectionSpec {
	return []experimentSectionSpec{
		{
			"Синтез материала",
			"получить порошок/прекурсор заданного состава",
			"Укажи исходные соли/прекурсоры, температуру/атмосферу и контроль фазового состава.",
		},
		{
			"Изготовление электрода",
			"сделать воспроизводимый электрод",
			"Укажи связующее, проводящую добавку, загрузку и сушку, если это есть в контексте.",
		},
		{
			"Сборка ячейки",
			"зафиксировать конфигурацию испытания",
			"Опиши тип ячейки, электролит, сепаратор и контрольный электрод без выдуманных чисел.",
		},
		{
			"Циклирование и диагностика",
			"измерить ёмкость, удержание и деградацию",
			"Включи EIS/CV/galvanostatic cycling, если они поддержаны источниками.",
		},
	}
}

func coatingCorrosionSections() []experimentSectionSpec {
	return []experimentSectionSpec{
		{
			"Подготовка подложки",
			"сделать сравнимые образцы",
			"Укажи материал, очистку/шероховатость и контрольную серию.",
		},
		{
			"Нанесение покрытия",
			"получить покрытие заданного состава и толщины",
			"Опиши метод нанесения, температуру/время/атмосферу и диапазоны варьирования.",
		},
		{
			"Контроль покрытия",
			"проверить структуру и адгезию до коррозии",
			"Включи толщину, пористость, SEM/EDS/XRD, scratch/adhesion test по контексту.",
		},
		{
			"Коррозионная проверка",
			"измерить скорость коррозии и механизм отказа",
			"Укажи среду, температуру, время, EIS/поляризацию/массовые потери, если они есть в источниках.",
		},
	}
}

func newAlloySections() []experimentSectionSpec {
	return []experimentSectionSpec{
		{
			"Состав и шихта",
			"зафиксировать химический состав и чистоту исходных материалов",
			"Укажи элементы/лигатуры, допуски и контроль состава.",
		},
		{
			"Плавка и получение образцов",
			"получить слиток/заготовку без смешения с проверкой свойства",
			"Опиши вакуум/инертную атмосферу, способ плавки/литья и число повторов, если поддержано контекстом.",
		},
		{
			"Термообработка или деформация",
			"сформировать требуемую микроструктуру",
			"Укажи температуру, время, охлаждение и диапазоны варьирования.",
		},
		{
			"Микроструктурный контроль",
			"проверить фазовый состав, зерно и выделения",
			"Включи SEM/TEM/XRD/DSC/EBSD только если это уместно для гипотезы.",
		},
		{
			"Механические испытания",
			"связать микроструктуру с KPI",
			"Укажи tensile/creep/hardness/fatigue и температуру испытания, если она есть в цели или источнике.",
		},
	}
}

func processRouteSections() []experimentSectionSpec {
	return []experimentSectionSpec{
		{
			"Базовый маршрут",
			"задать контрольный процесс для сравнения",
			"Опиши текущий режим, оборудование и измеряемый KPI.",
		},
		{
			"Изменяемые параметры",
			"выделить управляемые факторы процесса",
			"Для плавки, обработки, сверления или бурения укажи только параметры, поддержанные контекстом.",
		},
		{
			"Окно режимов",
			"проверить диапазоны без подмены результата предположениями",
			"Задай диапазоны варьирования и ограничения безопасности/оборудования.",
		},
		{
			"Контроль качества",
			"измерить дефекты, структуру и стабильность",
			"Опиши методы контроля, браковочные признаки и повторяемость.",
		},
		{
			"Сравнение с базой",
			"оценить прирост по KPI и побочные эффекты",
			"Сравни производительность, брак, стоимость, ресурс или свойство в зависимости от цели.",
		},
	}
}

func oreBeneficiationSections() []experimentSectionSpec {
	return []experimentSectionSpec{
		{
			"Характеристика питания",
			"зафиксировать состав и свойства исходного сырья",
			"Укажи гранулометрию, содержания металлов и минеральные формы из контекста.",
		},
		{
			"Схема опыта",
			"задать лабораторную схему обогащения",
			"Опиши лабораторную флотацию/гравитацию/магнитную сепарацию, число стадий и контрольную серию.",
		},
		{
			"Реагентный режим",
			"выделить управляемые факторы процесса",
			"Укажи реагенты, расходы, pH и время только из контекста, диапазоны — как варьирование.",
		},
		{
			"Баланс металла",
			"свести извлечение и качество по продуктам",
			"Посчитай выход, содержание и извлечение по концентрату и хвостам.",
		},
		{
			"Критерии извлечения",
			"связать результат опыта с KPI",
			"Задай измеримый порог по извлечению/качеству концентрата против базовой схемы.",
		},
	}
}

func metallurgyProcessSections() []experimentSectionSpec {
	return []experimentSectionSpec{
		{
			"Шихтовка",
			"зафиксировать состав шихты/раствора и исходные материалы",
			"Укажи компоненты, соотношения и контроль состава по контексту.",
		},
		{
			"Режим агрегата",
			"задать управляемые параметры передела",
			"Для плавки, выщелачивания, электролиза или обжига укажи температуру/время/расходы как диапазоны варьирования.",
		},
		{
			"Контроль состава",
			"проверить продукты передела",
			"Опиши пробоотбор и анализ состава продуктов и полупродуктов.",
		},
		{
			"Баланс металла",
			"оценить извлечение и потери против базы",
			"Сведи баланс по металлу и сравни извлечение и затраты с базовым режимом.",
		},
	}
}

func genericSections() []experimentSectionSpec {
	return []experimentSectionSpec{
		{"Объект и контроль", "описать материал/образец и базовую серию", "Укажи, с чем сравнивается гипотеза."},
		{"Варьируемое воздействие", "задать управляемые факторы", "Не придумывай диапазоны, которых нет в контексте."},
		{"Методы измерения", "проверить заявленное свойство", "Раздели характеризацию и испытания."},
		{"Критерий решения", "понять, подтверждена гипотеза или нет", "Критерий должен быть измеримым и связанным с KPI."},
	}
}

// parseExperimentPlan tolerantly extracts the JSON object from the model output.
func parseExperimentPlan(answer string) (experimentPlan, error) {
	start := strings.IndexByte(answer, '{')
	end := strings.LastIndexByte(answer, '}')
	if start < 0 || end <= start {
		return experimentPlan{}, errors.New("no JSON object in experiment-plan output")
	}
	raw := answer[start : end+1]
	var p experimentPlan
	if err := jsonx.Unmarshal([]byte(raw), &p); err != nil {
		// Tolerate unescaped LaTeX backslashes the model may include (see
		// repairJSONBackslashes); retry once before giving up.
		if err2 := jsonx.Unmarshal([]byte(repairJSONBackslashes(raw)), &p); err2 != nil {
			return experimentPlan{}, fmt.Errorf("parse experiment plan: %w", err)
		}
	}
	return p, nil
}

// normalizeCostLevel maps the model's cost label to low/medium/high, tolerating
// Russian variants; an unrecognised value yields "" (the UI omits the badge).
func normalizeCostLevel(s string) string {
	switch strings.ToLower(compactText(s)) {
	case levelLow, "низкая", "низкий", "низко":
		return levelLow
	case levelMedium, "средняя", "средний", "средне":
		return levelMedium
	case levelHigh, "высокая", "высокий", "высоко":
		return levelHigh
	default:
		return ""
	}
}

// normalizeTimeScale maps the model's time label to days/weeks/months, tolerating
// Russian variants; an unrecognised value yields "" (the UI omits the badge).
func normalizeTimeScale(s string) string {
	switch strings.ToLower(compactText(s)) {
	case timeDays, "дни", "день", "сутки":
		return timeDays
	case timeWeeks, "недели", "неделя":
		return timeWeeks
	case timeMonths, "месяцы", "месяц":
		return timeMonths
	default:
		return ""
	}
}

// cleanExperimentParams drops empty params and compacts whitespace.
func cleanExperimentParams(in []experimentParam) []map[string]string {
	out := make([]map[string]string, 0, len(in))
	for _, p := range in {
		name, rng := compactText(p.Name), compactText(p.Range)
		if name == "" && rng == "" {
			continue
		}
		out = append(out, map[string]string{keyName: name, "range": rng})
	}
	return out
}

func normalizeExperimentType(s string) string {
	switch strings.ToLower(compactText(s)) {
	case expTypeNewAlloy, "alloy", "сплав", "новый сплав":
		return expTypeNewAlloy
	case expTypeProcessRoute, "process", "route", stageProcess, "технологический маршрут":
		return expTypeProcessRoute
	case expTypeCoatingCorrosion, "coating", "corrosion", "покрытие", "коррозия":
		return expTypeCoatingCorrosion
	case expTypeBatteryMaterial, "battery", "cell", "катод", "анод", "батарейный материал":
		return expTypeBatteryMaterial
	case expTypeOreBeneficiation, "beneficiation", "флотация", "обогащение", "обогащение руды":
		return expTypeOreBeneficiation
	case expTypeMetallurgy, "metallurgy", "металлургия", "металлургический передел":
		return expTypeMetallurgy
	case expTypeGeneric, "общий":
		return expTypeGeneric
	default:
		return ""
	}
}

func cleanExperimentSections(in []experimentSection) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, s := range in {
		title, purpose := compactText(s.Title), compactText(s.Purpose)
		items := compactSlice(s.Items)
		if title == "" && purpose == "" && len(items) == 0 {
			continue
		}
		out = append(out, map[string]any{
			"title":   title,
			"purpose": purpose,
			keyItems:  items,
		})
	}
	return out
}

// mergeExperimentPlan stores the plan under detail.experiment_plan, preserving
// every other detail field (e.g. competitors, causal_chain, materials passport).
func mergeExperimentPlan(detail string, p experimentPlan, model string) string {
	m := map[string]any{}
	if detail != "" {
		_ = jsonx.Unmarshal([]byte(detail), &m)
	}
	plan := map[string]any{
		"experiment_type":          normalizeExperimentType(p.ExperimentType),
		"sections":                 cleanExperimentSections(p.Sections),
		"materials":                compactSlice(p.Materials),
		"process_parameters":       cleanExperimentParams(p.ProcessParams),
		"characterization_methods": compactSlice(p.Characterization),
		"test_methods":             compactSlice(p.TestMethods),
		"controls":                 compactSlice(p.Controls),
		"success_criteria":         compactSlice(p.SuccessCriteria),
		"estimated_cost":           normalizeCostLevel(p.EstimatedCost),
		"estimated_time":           normalizeTimeScale(p.EstimatedTime),
		"risks":                    compactSlice(p.Risks),
		keyModel:                   model,
		"planned_at":               time.Now().UTC().Format(time.RFC3339),
	}
	m["experiment_plan"] = plan
	b, err := jsonx.Marshal(m)
	if err != nil {
		return detail
	}
	return string(b)
}

// experimentSummary renders a short Russian revision summary from the plan.
func experimentSummary(p experimentPlan) string {
	steps := len(compactSlice(p.Materials)) + len(cleanExperimentParams(p.ProcessParams)) +
		len(compactSlice(p.TestMethods)) + len(cleanExperimentSections(p.Sections))
	if steps == 0 {
		return "План эксперимента составлен"
	}
	return fmt.Sprintf("План эксперимента составлен: %d пунктов", steps)
}
