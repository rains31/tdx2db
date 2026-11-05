package cmd

import (
	"context"
	"fmt"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/workflow"
)

type CronOptions struct {
	All          bool
	Day          bool
	Min          bool
	Basic        bool
	Block        bool
	Symbol       bool
	Gbbq         bool
	VipdocDir    string
	TargetDate   string
	MissingOnly  bool
	Force        bool
	DeleteEnable bool
}

func Cron(ctx context.Context, dbURI string, opts CronOptions) error {
	if opts.VipdocDir != "" {
		VipdocDir = opts.VipdocDir
	}

	db, err := database.NewDB(dbURI)
	if err != nil {
		return fmt.Errorf("failed to create database driver: %w", err)
	}

	if err := db.Connect(); err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := db.InitSchema(); err != nil {
		return fmt.Errorf("failed to initialize schema: %w", err)
	}

	if err := checkSchemaVersion(db); err != nil {
		return err
	}

	defer db.Close()

	if err := ctx.Err(); err != nil {
		return err
	}

	today := GetToday()

	// 开关决策：是否显式指定了任意一个特定类型
	hasSpecific := opts.Day || opts.Min || opts.Basic || opts.Block || opts.Symbol || opts.Gbbq

	var day, min, basic, block, symbol, gbbq bool
	if hasSpecific && !opts.All {
		// 精准模式：只执行用户明确开启的类型
		day = opts.Day
		min = opts.Min
		basic = opts.Basic
		block = opts.Block
		symbol = opts.Symbol
		gbbq = opts.Gbbq
	} else {
		// 全量/默认日常调度模式
		day = true
		min = opts.Min || opts.All
		basic = true
		block = true
		symbol = true
		gbbq = opts.Gbbq || opts.All
	}

	plan, err := workflow.BuildWorkPlan(db, today, day, min, basic, opts.TargetDate, opts.MissingOnly, opts.Force, gbbq)
	if err != nil {
		return err
	}
	if plan.Reason != "" {
		fmt.Println(plan.Reason)
	}
	if !plan.AnyNeeded() && !block && !symbol {
		return nil
	}

	executor := workflow.NewTaskExecutor(db, workflow.GetRegisteredTasks())

	args := &workflow.TaskArgs{
		Day:         day,
		Min:         min,
		Basic:       basic,
		Block:       block,
		Symbol:      symbol,
		Gbbq:        gbbq,
		All:         opts.All,
		MissingOnly: opts.MissingOnly,
		Force:       opts.Force,
		Delete:      opts.DeleteEnable,
		TempDir:     TempDir,
		VipdocDir:   VipdocDir,
		Today:       today,
		TargetDate:  opts.TargetDate,
		Plan:        plan,
	}

	taskNames := workflow.GetUpdateTaskNames()

	if err := executor.Run(ctx, taskNames, args); err != nil {
		return fmt.Errorf("workflow execution failed: %w", err)
	}

	fmt.Println("🚀 今日任务执行成功")
	return nil
}

