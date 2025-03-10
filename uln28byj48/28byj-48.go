// Package uln28byj48 implements a GPIO based
// stepper motor (model: 28byj-48) with uln2003 controler.
package uln28byj48

/*
	Motor Name:  		28byj-48
	Motor Controler: 	ULN2003
	Datasheet:
			ULN2003: 	https://www.makerguides.com/wp-content/uploads/2019/04/ULN2003-Datasheet.pdf
			28byj-48:	https://components101.com/sites/default/files/component_datasheet/28byj48-step-motor-datasheet.pdf

	This driver will drive the motor with half-step driving method (instead of full-step drive) for higher resolutions.
	In half-step the current vector divides a circle into eight parts. The eight step switching sequence is shown in
	stepSequence below. The motor takes 5.625*(1/64)° per step. For 360° the motor will take 4096 steps.

    The motor can run at a max speed of ~146rpm. Though it is recommended to not run the motor at max speed as it can
	damage the gears. The max rpm of the motor shaft after gear reduction is ~15rpm.
*/

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/multierr"
	"go.viam.com/rdk/components/board"
	"go.viam.com/rdk/components/motor"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/operation"
	"go.viam.com/rdk/resource"
	"go.viam.com/utils"
)

var (
	// Model for viam supported uln2003 28byj-48 sensor.
	Model                = resource.NewModel("viam", "uln2003", "28byj-48")
	minDelayBetweenTicks = 100 * time.Microsecond // minimum sleep time between each ticks
	maxRPM               = 15.0                   // max rpm of the 28byj-48 motor after gear reduction
)

// stepSequence contains switching signal for uln2003 pins.
// Treversing through stepSequence once is one step.
var stepSequence = [8][4]bool{
	{false, false, false, true},
	{true, false, false, true},
	{true, false, false, false},
	{true, true, false, false},
	{false, true, false, false},
	{false, true, true, false},
	{false, false, true, false},
	{false, false, true, true},
}

// PinConfig defines the mapping of where motor are wired.
type PinConfig struct {
	In1 string `json:"in1"`
	In2 string `json:"in2"`
	In3 string `json:"in3"`
	In4 string `json:"in4"`
}

// Config describes the configuration of a motor.
type Config struct {
	Pins             PinConfig `json:"pins"`
	BoardName        string    `json:"board"`
	TicksPerRotation int       `json:"ticks_per_rotation"`
}

// Validate ensures all parts of the config are valid.
func (conf *Config) Validate(path string) ([]string, error) {
	var deps []string
	if conf.BoardName == "" {
		return nil, resource.NewConfigValidationFieldRequiredError(path, "board")
	}

	if conf.Pins.In1 == "" {
		return nil, resource.NewConfigValidationFieldRequiredError(path, "in1")
	}

	if conf.Pins.In2 == "" {
		return nil, resource.NewConfigValidationFieldRequiredError(path, "in2")
	}

	if conf.Pins.In3 == "" {
		return nil, resource.NewConfigValidationFieldRequiredError(path, "in3")
	}

	if conf.Pins.In4 == "" {
		return nil, resource.NewConfigValidationFieldRequiredError(path, "in4")
	}

	deps = append(deps, conf.BoardName)
	return deps, nil
}

func init() {
	resource.RegisterComponent(motor.API, Model, resource.Registration[motor.Motor, *Config]{
		Constructor: new28byj,
	})
}

func new28byj(
	ctx context.Context, deps resource.Dependencies, conf resource.Config, logger logging.Logger,
) (motor.Motor, error) {
	mc, err := resource.NativeConfig[*Config](conf)
	if err != nil {
		return nil, err
	}

	b, err := board.FromDependencies(deps, mc.BoardName)
	if err != nil {
		return nil, errors.Wrap(err, "expected board name in config for motor")
	}

	if mc.TicksPerRotation <= 0 {
		return nil, errors.New("expected ticks_per_rotation to be greater than zero in config for motor")
	}

	m := &uln28byj{
		Named:            conf.ResourceName().AsNamed(),
		theBoard:         b,
		ticksPerRotation: mc.TicksPerRotation,
		logger:           logger,
		motorName:        conf.Name,
		opMgr:            operation.NewSingleOperationManager(),
	}

	in1, err := b.GPIOPinByName(mc.Pins.In1)
	if err != nil {
		return nil, errors.Wrapf(err, "in in1 in motor (%s)", m.motorName)
	}
	m.in1 = in1

	in2, err := b.GPIOPinByName(mc.Pins.In2)
	if err != nil {
		return nil, errors.Wrapf(err, "in in2 in motor (%s)", m.motorName)
	}
	m.in2 = in2

	in3, err := b.GPIOPinByName(mc.Pins.In3)
	if err != nil {
		return nil, errors.Wrapf(err, "in in3 in motor (%s)", m.motorName)
	}
	m.in3 = in3

	in4, err := b.GPIOPinByName(mc.Pins.In4)
	if err != nil {
		return nil, errors.Wrapf(err, "in in4 in motor (%s)", m.motorName)
	}
	m.in4 = in4

	return m, nil
}

// struct is named after the controler uln28byj.
type uln28byj struct {
	resource.Named
	resource.AlwaysRebuild
	theBoard           board.Board
	ticksPerRotation   int
	in1, in2, in3, in4 board.GPIOPin
	logger             logging.Logger
	motorName          string

	// state
	workers   *utils.StoppableWorkers
	lock      sync.Mutex
	opMgr     *operation.SingleOperationManager
	doRunDone func()

	stepPosition       int64
	stepperDelay       time.Duration
	targetStepPosition int64
}

// doRun runs the motor till it reaches target step position.
func (m *uln28byj) doRun() {
	// cancel doRun if it already exists
	if m.doRunDone != nil {
		m.doRunDone()
	}

	// start a new doRun
	var doRunCtx context.Context
	doRunCtx, m.doRunDone = context.WithCancel(context.Background())
	m.workers = utils.NewBackgroundStoppableWorkers(func(ctx context.Context) {
		for {
			select {
			case <-doRunCtx.Done():
				return
			default:
			}

			if m.getStepPosition() == m.getTargetStepPosition() {
				if err := m.doStop(doRunCtx); err != nil {
					m.logger.Errorf("error setting pins to zero %v", err)
					return
				}
			} else {
				err := m.doStep(doRunCtx, m.getStepPosition() < m.getTargetStepPosition())
				if err != nil {
					m.logger.Errorf("error stepping %v", err)
					return
				}
			}
		}
	})
}

// doStop sets all the pins to 0 to stop the motor.
func (m *uln28byj) doStop(ctx context.Context) error {
	m.lock.Lock()
	defer m.lock.Unlock()
	return m.setPins(ctx, [4]bool{false, false, false, false})
}

// Depending on the direction, doStep will either treverse the stepSequence array in ascending
// or descending order.
func (m *uln28byj) doStep(ctx context.Context, forward bool) error {
	m.lock.Lock()
	defer m.lock.Unlock()
	if forward {
		m.stepPosition++
	} else {
		m.stepPosition--
	}

	var nextStepSequence int
	if m.stepPosition < 0 {
		nextStepSequence = 7 + int(m.stepPosition%8)
	} else {
		nextStepSequence = int(m.stepPosition % 8)
	}

	err := m.setPins(ctx, stepSequence[nextStepSequence])
	if err != nil {
		return err
	}

	time.Sleep(m.stepperDelay)
	return nil
}

// doTicks sets all 4 pins.
// must be called in locked context.
func (m *uln28byj) setPins(ctx context.Context, pins [4]bool) error {
	err := multierr.Combine(
		m.in1.Set(ctx, pins[0], nil),
		m.in2.Set(ctx, pins[1], nil),
		m.in3.Set(ctx, pins[2], nil),
		m.in4.Set(ctx, pins[3], nil),
	)

	return err
}

func (m *uln28byj) getTargetStepPosition() int64 {
	m.lock.Lock()
	defer m.lock.Unlock()
	return m.targetStepPosition
}

func (m *uln28byj) setTargetStepPosition(targetPos int64) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.targetStepPosition = targetPos
}

func (m *uln28byj) getStepPosition() int64 {
	m.lock.Lock()
	defer m.lock.Unlock()
	return m.stepPosition
}

func (m *uln28byj) setStepperDelay(delay time.Duration) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.stepperDelay = delay
}

// GoFor instructs the motor to go in a specific direction for a specific amount of
// revolutions at a given speed in revolutions per minute. Both the RPM and the revolutions
// can be assigned negative values to move in a backwards direction. Note: if both are negative
// the motor will spin in the forward direction.
func (m *uln28byj) GoFor(ctx context.Context, rpm, revolutions float64, extra map[string]interface{}) error {
	ctx, done := m.opMgr.New(ctx)
	defer done()

	warning, err := motor.CheckSpeed(rpm, maxRPM)
	if warning != "" {
		m.logger.CWarn(ctx, warning)
		if err != nil {
			m.logger.CError(ctx, err)
			// only stop if we receive a zero RPM error
			return m.Stop(ctx, extra)
		}
		// we do not get the zeroRPM error, but still want
		// the motor to move at the maximum rpm
		m.logger.CWarnf(ctx, "can only move at maxRPM of %v", maxRPM)
		rpm = maxRPM * motor.GetSign(rpm)
	}

	targetStepPosition, stepperDelay := m.goMath(rpm, revolutions)
	m.setTargetStepPosition(targetStepPosition)
	m.setStepperDelay(stepperDelay)
	m.doRun()

	positionReached := func(ctx context.Context) (bool, error) {
		return m.getTargetStepPosition() == m.getStepPosition(), nil
	}

	err = m.opMgr.WaitForSuccess(
		ctx,
		m.stepperDelay,
		positionReached,
	)
	// Ignore the context canceled error - this occurs when the motor is stopped
	// at the beginning of goForInternal
	if !errors.Is(err, context.Canceled) {
		return err
	}

	return nil
}

func (m *uln28byj) goMath(rpm, revolutions float64) (int64, time.Duration) {
	var d int64 = 1

	if math.Signbit(revolutions) != math.Signbit(rpm) {
		d = -1
	}

	revolutions = math.Abs(revolutions)
	rpm = math.Abs(rpm) * float64(d)

	targetPosition := m.getStepPosition() + int64(float64(d)*revolutions*float64(m.ticksPerRotation))
	stepperDelay := m.calcStepperDelay(rpm)

	return targetPosition, stepperDelay
}

func (m *uln28byj) calcStepperDelay(rpm float64) time.Duration {
	stepperDelay := time.Duration(int64((1/(math.Abs(rpm)*float64(m.ticksPerRotation)/60.0))*1000000)) * time.Microsecond
	if stepperDelay < minDelayBetweenTicks {
		m.logger.Debugf("Computed sleep time between ticks (%v) too short. Defaulting to %v", stepperDelay, minDelayBetweenTicks)
		stepperDelay = minDelayBetweenTicks
	}
	return stepperDelay
}

// GoTo instructs the motor to go to a specific position (provided in revolutions from home/zero),
// at a specific RPM. Regardless of the directionality of the RPM this function will move the motor
// towards the specified target.
func (m *uln28byj) GoTo(ctx context.Context, rpm, positionRevolutions float64, extra map[string]interface{}) error {
	curPos, err := m.Position(ctx, extra)
	if err != nil {
		return errors.Wrapf(err, "error in GoTo from motor (%s)", m.motorName)
	}
	moveDistance := positionRevolutions - curPos

	m.logger.CDebugf(ctx, "Moving %v ticks at %v rpm", moveDistance, rpm)

	if moveDistance == 0 {
		return nil
	}

	return m.GoFor(ctx, math.Abs(rpm), moveDistance, extra)
}

// SetRPM instructs the motor to move at the specified RPM indefinitely.
func (m *uln28byj) SetRPM(ctx context.Context, rpm float64, extra map[string]interface{}) error {
	powerPct := rpm / maxRPM
	return m.SetPower(ctx, powerPct, extra)
}

// Set the current position (+/- offset) to be the new zero (home) position.
func (m *uln28byj) ResetZeroPosition(ctx context.Context, offset float64, extra map[string]interface{}) error {
	newPosition := int64(-1 * offset * float64(m.ticksPerRotation))
	// use Stop to set the target position to the current position again
	if err := m.Stop(ctx, extra); err != nil {
		return err
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	m.stepPosition = newPosition
	m.targetStepPosition = newPosition
	return nil
}

// SetPower is invalid for this motor.
func (m *uln28byj) SetPower(ctx context.Context, powerPct float64, extra map[string]interface{}) error {
	ctx, done := m.opMgr.New(ctx)
	defer done()

	warning, err := motor.CheckSpeed(powerPct*maxRPM, maxRPM)
	if warning != "" {
		m.logger.CWarn(ctx, warning)
		if err != nil {
			m.logger.CError(ctx, err)
			// only stop if we receive a zero RPM error
			return m.Stop(ctx, extra)
		}
	}

	m.lock.Lock()
	defer m.lock.Unlock()
	direction := motor.GetSign(powerPct) // get the direction to set target to -ve/+ve Inf
	m.targetStepPosition = int64(math.Inf(int(direction)))
	powerPct = motor.ClampPower(powerPct) // ensure 1.0 max and -1.0 min
	m.stepperDelay = m.calcStepperDelay(powerPct * maxRPM)

	m.doRun()

	return nil
}

// Position reports the current step position of the motor. If it's not supported, the returned
// data is undefined.
func (m *uln28byj) Position(ctx context.Context, extra map[string]interface{}) (float64, error) {
	m.lock.Lock()
	defer m.lock.Unlock()
	return float64(m.stepPosition) / float64(m.ticksPerRotation), nil
}

// Properties returns the status of whether the motor supports certain optional properties.
func (m *uln28byj) Properties(ctx context.Context, extra map[string]interface{}) (motor.Properties, error) {
	return motor.Properties{
		PositionReporting: true,
	}, nil
}

// IsMoving returns if the motor is currently moving.
func (m *uln28byj) IsMoving(ctx context.Context) (bool, error) {
	m.lock.Lock()
	defer m.lock.Unlock()
	return m.stepPosition != m.targetStepPosition, nil
}

// Stop turns the power to the motor off immediately, without any gradual step down.
func (m *uln28byj) Stop(ctx context.Context, extra map[string]interface{}) error {
	if m.doRunDone != nil {
		m.doRunDone()
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	m.targetStepPosition = m.stepPosition
	return nil
}

// IsPowered returns whether or not the motor is currently on. It also returns the percent power
// that the motor has, but stepper motors only have this set to 0% or 100%, so it's a little
// redundant.
func (m *uln28byj) IsPowered(ctx context.Context, extra map[string]interface{}) (bool, float64, error) {
	on, err := m.IsMoving(ctx)
	if err != nil {
		return on, 0.0, errors.Wrapf(err, "error in IsPowered from motor (%s)", m.motorName)
	}
	percent := 0.0
	if on {
		percent = 1.0
	}
	return on, percent, err
}

func (m *uln28byj) Close(ctx context.Context) error {
	if err := m.Stop(ctx, nil); err != nil {
		return err
	}
	m.workers.Stop()
	return nil
}
