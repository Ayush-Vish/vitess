/*
Copyright 2023 The Vitess Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package operators

import (
	"fmt"
	"slices"
	"strings"

	"vitess.io/vitess/go/slice"
	"vitess.io/vitess/go/vt/sqlparser"
	"vitess.io/vitess/go/vt/vterrors"
	"vitess.io/vitess/go/vt/vtgate/evalengine"
	"vitess.io/vitess/go/vt/vtgate/planbuilder/plancontext"
	"vitess.io/vitess/go/vt/vtgate/semantics"
)

// Projection is used when we need to evaluate expressions on the vtgate
// It uses the evalengine to accomplish its goal
type Projection struct {
	unaryOperator

	// Columns contain the expressions as viewed from the outside of this operator
	Columns ProjCols

	// DT will hold all the necessary information if this is a derived table projection
	DT       *DerivedTable
	FromAggr bool
}

type (
	DerivedTable struct {
		TableID semantics.TableSet
		Alias   string
		Columns sqlparser.Columns
	}
)

func (dt *DerivedTable) String() string {
	return fmt.Sprintf("DERIVED %s(%s)", dt.Alias, sqlparser.String(dt.Columns))
}

func (dt *DerivedTable) RewriteExpression(ctx *plancontext.PlanningContext, expr sqlparser.Expr) sqlparser.Expr {
	if dt == nil {
		return expr
	}
	tableInfo, err := ctx.SemTable.TableInfoFor(dt.TableID)
	if err != nil {
		panic(err)
	}
	return semantics.RewriteDerivedTableExpression(expr, tableInfo)
}

func (dt *DerivedTable) introducesTableID() semantics.TableSet {
	if dt == nil {
		return semantics.EmptyTableSet()
	}
	return dt.TableID
}

type (
	// ProjCols is used to enable projections that are only valid if we can push them into a route, and we never need to ask it about offsets
	ProjCols interface {
		GetColumns() []*sqlparser.AliasedExpr
		GetSelectExprs() []sqlparser.SelectExpr
	}

	// Used when there are stars in the expressions that we were unable to expand
	StarProjections []sqlparser.SelectExpr

	// Used when we know all the columns
	AliasedProjections []*ProjExpr

	ProjExpr struct {
		Original *sqlparser.AliasedExpr // this is the expression the user asked for. should only be used to decide on the column alias
		EvalExpr sqlparser.Expr         // EvalExpr represents the expression evaluated at runtime or used when the ProjExpr is pushed under a route
		ColExpr  sqlparser.Expr         // ColExpr is used during planning to figure out which column this ProjExpr is representing
		Info     ExprInfo               // Here we store information about evalengine, offsets or subqueries
	}
)

type (
	ExprInfo interface {
		expr()
	}

	// Offset is used when we are only passing through data from an incoming column
	Offset int

	// EvalEngine is used for expressions that have to be evaluated in the vtgate using the evalengine
	EvalEngine struct {
		EExpr evalengine.Expr
	}

	SubQueryExpression []*SubQuery
)

func newProjExpr(ae *sqlparser.AliasedExpr) *ProjExpr {
	return &ProjExpr{
		Original: sqlparser.Clone(ae),
		EvalExpr: ae.Expr,
		ColExpr:  ae.Expr,
	}
}

func newProjExprWithInner(ae *sqlparser.AliasedExpr, in sqlparser.Expr) *ProjExpr {
	return &ProjExpr{
		Original: ae,
		EvalExpr: in,
		ColExpr:  ae.Expr,
	}
}

func newAliasedProjection(src Operator) *Projection {
	return &Projection{
		unaryOperator: newUnaryOp(src),
		Columns:       AliasedProjections{},
	}
}

func (sp StarProjections) GetColumns() []*sqlparser.AliasedExpr {
	panic(vterrors.VT09015())
}

func (sp StarProjections) GetSelectExprs() []sqlparser.SelectExpr {
	return sp
}

func (ap AliasedProjections) GetColumns() []*sqlparser.AliasedExpr {
	return slice.Map(ap, func(from *ProjExpr) *sqlparser.AliasedExpr {
		return from.Original
	})
}

func (ap AliasedProjections) GetSelectExprs() []sqlparser.SelectExpr {
	return slice.Map(ap, func(from *ProjExpr) sqlparser.SelectExpr {
		return aeWrap(from.ColExpr)
	})
}

func (pe *ProjExpr) String() string {
	var alias, expr, info string
	if pe.Original.As.NotEmpty() {
		alias = " AS " + pe.Original.As.String()
	}
	if sqlparser.Equals.Expr(pe.EvalExpr, pe.ColExpr) {
		expr = sqlparser.String(pe.EvalExpr)
	} else {
		expr = fmt.Sprintf("%s|%s", sqlparser.String(pe.EvalExpr), sqlparser.String(pe.ColExpr))
	}
	switch pe.Info.(type) {
	case Offset:
		info = " [O]"
	case *EvalEngine:
		info = " [E]"
	case SubQueryExpression:
		info = " [SQ]"
	}

	return expr + alias + info
}

func (pe *ProjExpr) isSameInAndOut(ctx *plancontext.PlanningContext) bool {
	return ctx.SemTable.EqualsExprWithDeps(pe.EvalExpr, pe.ColExpr)
}

var _ selectExpressions = (*Projection)(nil)

// createSimpleProjection returns a projection where all columns are offsets.
// used to change the name and order of the columns in the final output
func createSimpleProjection(ctx *plancontext.PlanningContext, selExprs []sqlparser.SelectExpr, src Operator) *Projection {
	p := newAliasedProjection(src)
	for _, e := range selExprs {
		ae, isAe := e.(*sqlparser.AliasedExpr)
		if !isAe {
			panic(vterrors.VT09015())
		}

		if ae.As.IsEmpty() {
			// if we don't have an alias, we can use the column name as the alias
			// the expectation is that when users use columns without aliases, they want the column name as the alias
			// for more complex expressions, we just assume they'll use column offsets instead of column names
			col, ok := ae.Expr.(*sqlparser.ColName)
			if ok {
				ae.As = col.Name
			}
		}

		offset := p.Source.AddColumn(ctx, true, false, ae)
		expr := newProjExpr(ae)
		expr.Info = Offset(offset)
		p.addProjExpr(expr)
	}
	return p
}

// canPush returns false if the projection has subquery expressions in it and the subqueries have not yet
// been settled. Once they have settled, we know where to push the projection, but if we push too early
// the projection can end up in the wrong branch of joins
func (p *Projection) canPush(ctx *plancontext.PlanningContext) bool {
	if reachedPhase(ctx, subquerySettling) {
		return true
	}
	ap, ok := p.Columns.(AliasedProjections)
	if !ok {
		// we can't mix subqueries and unexpanded stars, so we know this does not contain any subqueries
		return true
	}
	for _, projection := range ap {
		if _, ok := projection.Info.(SubQueryExpression); ok {
			return false
		}
	}
	return true
}

func (p *Projection) GetAliasedProjections() (AliasedProjections, error) {
	switch cols := p.Columns.(type) {
	case AliasedProjections:
		return cols, nil
	case nil:
		return nil, nil
	default:
		return nil, vterrors.VT09015()
	}
}

func (p *Projection) isDerived() bool {
	return p.DT != nil
}

func (p *Projection) derivedName() string {
	if p.DT == nil {
		return ""
	}

	return p.DT.Alias
}

func (p *Projection) FindCol(ctx *plancontext.PlanningContext, expr sqlparser.Expr, underRoute bool) int {
	ap, err := p.GetAliasedProjections()
	if err != nil {
		panic(err)
	}

	if underRoute && p.isDerived() {
		return -1
	}

	for offset, pe := range ap {
		if ctx.SemTable.EqualsExprWithDeps(pe.ColExpr, expr) {
			return offset
		}
	}

	return -1
}

func (p *Projection) addProjExpr(pe ...*ProjExpr) int {
	ap, err := p.GetAliasedProjections()
	if err != nil {
		panic(err)
	}

	offset := len(ap)
	p.Columns = append(ap, pe...)

	return offset
}

func (p *Projection) addUnexploredExpr(ae *sqlparser.AliasedExpr, e sqlparser.Expr) int {
	return p.addProjExpr(newProjExprWithInner(ae, e))
}

func (p *Projection) addSubqueryExpr(ctx *plancontext.PlanningContext, ae *sqlparser.AliasedExpr, expr sqlparser.Expr, sqs ...*SubQuery) {
	ap, err := p.GetAliasedProjections()
	if err != nil {
		panic(err)
	}
	for _, projExpr := range ap {
		if ctx.SemTable.EqualsExprWithDeps(projExpr.EvalExpr, expr) {
			// if we already have this column, we can just return the offset
			return
		}
	}

	pe := newProjExprWithInner(ae, expr)
	pe.Info = SubQueryExpression(sqs)

	_ = p.addProjExpr(pe)
}

func (p *Projection) addColumnWithoutPushing(ctx *plancontext.PlanningContext, expr *sqlparser.AliasedExpr, _ bool) int {
	return p.addColumn(ctx, true, false, expr, false)
}

func (p *Projection) AddWSColumn(ctx *plancontext.PlanningContext, offset int, underRoute bool) int {
	cols, aliased := p.Columns.(AliasedProjections)
	if !aliased {
		panic(vterrors.VT09015())
	}

	if offset >= len(cols) || offset < 0 {
		panic(vterrors.VT13001(fmt.Sprintf("offset [%d] out of range [%d]", offset, len(cols))))
	}

	expr := cols[offset].EvalExpr
	ws := weightStringFor(expr)
	if offset := p.FindCol(ctx, ws, underRoute); offset >= 0 {
		// if we already have this column, we can just return the offset
		return offset
	}

	aeWs := aeWrap(ws)
	pe := newProjExprWithInner(aeWs, ws)
	if underRoute {
		return p.addProjExpr(pe)
	}

	// we need to push down this column to our input
	offsetOnInput := p.Source.FindCol(ctx, expr, false)
	if offsetOnInput >= 0 {
		// if we are not getting this from the source, we can solve this at offset planning time
		inputOffset := p.Source.AddWSColumn(ctx, offsetOnInput, false)
		pe.Info = Offset(inputOffset)
	}

	return p.addProjExpr(pe)
}

func (p *Projection) AddColumn(ctx *plancontext.PlanningContext, reuse bool, addToGroupBy bool, ae *sqlparser.AliasedExpr) int {
	return p.addColumn(ctx, reuse, addToGroupBy, ae, true)
}

func (p *Projection) addColumn(
	ctx *plancontext.PlanningContext,
	reuse bool,
	addToGroupBy bool,
	ae *sqlparser.AliasedExpr,
	push bool,
) int {
	expr := p.DT.RewriteExpression(ctx, ae.Expr)

	if reuse {
		offset := p.FindCol(ctx, expr, false)
		if offset >= 0 {
			return offset
		}
	}

	// ok, we need to add the expression. let's check if we should rewrite a ws expression first
	ws, ok := expr.(*sqlparser.WeightStringFuncExpr)
	if ok {
		cols, ok := p.Columns.(AliasedProjections)
		if !ok {
			panic(vterrors.VT09015())
		}
		for _, projExpr := range cols {
			if ctx.SemTable.EqualsExprWithDeps(ws.Expr, projExpr.ColExpr) {
				// if someone is asking for the ws of something we are projecting,
				// we need push down the ws of the eval expression
				ws.Expr = projExpr.EvalExpr
			}
		}
	}

	pe := newProjExprWithInner(ae, expr)
	if !push {
		return p.addProjExpr(pe)
	}

	var inputOffset int
	if nothingNeedsFetching(ctx, expr) {
		// if we don't need to fetch anything, we could just evaluate it in the projection
		// we still check if it's there - if it is, we can, we should use it
		inputOffset = p.Source.FindCol(ctx, expr, false)
		if inputOffset < 0 {
			return p.addProjExpr(pe)
		}
	} else {
		// we need to push down this column to our input
		inputOffset = p.Source.AddColumn(ctx, true, addToGroupBy, ae)
	}

	pe.Info = Offset(inputOffset) // since we already know the offset, let's save the information
	return p.addProjExpr(pe)
}

func (po Offset) expr()             {}
func (po *EvalEngine) expr()        {}
func (po SubQueryExpression) expr() {}

func (p *Projection) Clone(inputs []Operator) Operator {
	klone := *p
	klone.Source = inputs[0]
	return &klone
}

func (p *Projection) AddPredicate(ctx *plancontext.PlanningContext, expr sqlparser.Expr) Operator {
	// we just pass through the predicate to our source
	p.Source = p.Source.AddPredicate(ctx, expr)
	return p
}

func (p *Projection) GetColumns(*plancontext.PlanningContext) []*sqlparser.AliasedExpr {
	return p.Columns.GetColumns()
}

func (p *Projection) GetSelectExprs(*plancontext.PlanningContext) []sqlparser.SelectExpr {
	switch cols := p.Columns.(type) {
	case StarProjections:
		return cols
	case AliasedProjections:
		var output []sqlparser.SelectExpr
		for _, pe := range cols {
			ae := &sqlparser.AliasedExpr{Expr: pe.EvalExpr}
			if pe.Original.As.NotEmpty() {
				ae.As = pe.Original.As
			} else if !sqlparser.Equals.Expr(ae.Expr, pe.Original.Expr) {
				ae.As = sqlparser.NewIdentifierCI(pe.Original.ColumnName())
			}
			output = append(output, ae)
		}
		return output
	default:
		panic("unknown type")
	}
}

func (p *Projection) GetOrdering(ctx *plancontext.PlanningContext) []OrderBy {
	return p.Source.GetOrdering(ctx)
}

// AllOffsets returns a slice of integer offsets for all columns in the Projection
// if all columns are of type Offset. If any column is not of type Offset, it returns nil.
func (p *Projection) AllOffsets() (cols []int, colNames []string) {
	ap, err := p.GetAliasedProjections()
	if err != nil {
		return nil, nil
	}
	for _, c := range ap {
		offset, ok := c.Info.(Offset)
		if !ok {
			return nil, nil
		}
		colName := ""
		if c.Original.As.NotEmpty() {
			colName = c.Original.As.String()
		}

		cols = append(cols, int(offset))
		colNames = append(colNames, colName)
	}
	return
}

func (p *Projection) ShortDescription() string {
	var result []string
	if p.DT != nil {
		result = append(result, p.DT.String())
	}

	switch columns := p.Columns.(type) {
	case StarProjections:
		for _, se := range columns {
			result = append(result, sqlparser.String(se))
		}
	case AliasedProjections:
		for _, col := range columns {
			result = append(result, col.String())
		}
	}

	return strings.Join(result, ", ")
}

func (p *Projection) isNeeded() bool {
	ap, err := p.GetAliasedProjections()
	if err != nil {
		return true // we don't know enough about the columns to make a decision
	}

	for i, projection := range ap {
		e, ok := projection.Info.(Offset)
		if !ok || int(e) != i || projection.Original.As.NotEmpty() {
			return true
		}
	}

	return false
}

func (p *Projection) Compact(ctx *plancontext.PlanningContext) (Operator, *ApplyResult) {
	if !p.isNeeded() {
		return p.Source, Rewrote("removed projection only passing through the input")
	}

	switch src := p.Source.(type) {
	case *ApplyJoin:
		return p.compactWithJoin(ctx, src)
	}
	return p, NoRewrite
}

func (p *Projection) compactWithJoin(ctx *plancontext.PlanningContext, join *ApplyJoin) (Operator, *ApplyResult) {
	ap, err := p.GetAliasedProjections()
	if err != nil || len(join.Columns) == 0 {
		return p, NoRewrite
	}

	var newColumns []int
	newColumnsAST := &applyJoinColumns{}
	for _, col := range ap {
		switch colInfo := col.Info.(type) {
		case Offset:
			if col.Original.As.NotEmpty() {
				return p, NoRewrite
			}
			newColumns = append(newColumns, join.Columns[colInfo])
			newColumnsAST.add(join.JoinColumns.columns[colInfo])
		case nil:
			if !ctx.SemTable.EqualsExprWithDeps(col.EvalExpr, col.ColExpr) {
				// the inner expression is different from what we are presenting to the outside - this means we need to evaluate
				return p, NoRewrite
			}
			offset := slices.IndexFunc(join.JoinColumns.columns, applyJoinCompare(ctx, col.ColExpr))
			if offset < 0 {
				return p, NoRewrite
			}
			if len(join.Columns) > 0 {
				newColumns = append(newColumns, join.Columns[offset])
			}
			newColumnsAST.add(join.JoinColumns.columns[offset])
		default:
			return p, NoRewrite
		}
	}
	join.Columns = newColumns
	join.JoinColumns = newColumnsAST
	return join, Rewrote("remove projection from before join")
}

// needsEvaluation finds the expression given by this argument and checks if the inside and outside expressions match
// we can't rely on the content of the info field since it's not filled in until offset plan time
func (p *Projection) needsEvaluation(ctx *plancontext.PlanningContext, e sqlparser.Expr) bool {
	ap, err := p.GetAliasedProjections()
	if err != nil {
		return true
	}

	for _, pe := range ap {
		if !ctx.SemTable.EqualsExprWithDeps(pe.ColExpr, e) {
			continue
		}
		return !ctx.SemTable.EqualsExprWithDeps(pe.ColExpr, pe.EvalExpr)
	}
	return false
}

func (p *Projection) planOffsets(ctx *plancontext.PlanningContext) Operator {
	ap, err := p.GetAliasedProjections()
	if err != nil {
		panic(err)
	}

	for _, pe := range ap {
		switch pe.Info.(type) {
		case Offset:
			pe.EvalExpr = useOffsets(ctx, pe.EvalExpr, p)
			continue
		case *EvalEngine:
			continue
		}

		// first step is to replace the expressions we expect to get from our input with the offsets for these
		rewritten := useOffsets(ctx, pe.EvalExpr, p)
		pe.EvalExpr = rewritten

		// if we get a pure offset back. No need to do anything else
		offset, ok := rewritten.(*sqlparser.Offset)
		if ok {
			pe.Info = Offset(offset.V)
			continue
		}

		// for everything else, we'll turn to the evalengine
		eexpr, err := evalengine.Translate(rewritten, &evalengine.Config{
			ResolveType: ctx.TypeForExpr,
			Collation:   ctx.SemTable.Collation,
			Environment: ctx.VSchema.Environment(),
		})
		if err != nil {
			panic(err)
		}

		pe.Info = &EvalEngine{
			EExpr: eexpr,
		}
	}
	return nil
}

func (p *Projection) introducesTableID() semantics.TableSet {
	return p.DT.introducesTableID()
}
