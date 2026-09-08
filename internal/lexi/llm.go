package lexi

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"
)

const (
	slowLatencyFactor = 5
	simulatedReply    = "Recebemos sua mensagem e já estamos preparando uma proposta de acordo."
)

var errLLMUnavailable = errors.New("llm: upstream unavailable")

type LLM interface {
	Reply(ctx context.Context, prompt string) (string, error)
}

type LLMSimulation struct {
	MinLatency  time.Duration
	MaxLatency  time.Duration
	FailureRate float64
	SlowRate    float64
}

type SimulatedLLM struct {
	sim LLMSimulation
	mu  sync.Mutex
	rng *rand.Rand
}

func NewSimulatedLLM(sim LLMSimulation, seed uint64) *SimulatedLLM {
	return &SimulatedLLM{
		sim: sim,
		rng: rand.New(rand.NewPCG(seed, seed)),
	}
}

func (s *SimulatedLLM) Reply(ctx context.Context, prompt string) (string, error) {
	branchRoll, latencyRoll := s.draws()
	switch {
	case branchRoll < s.sim.FailureRate:
		if err := sleep(ctx, s.normalLatency(latencyRoll)); err != nil {
			return "", err
		}
		return "", errLLMUnavailable
	case branchRoll < s.sim.FailureRate+s.sim.SlowRate:
		if err := sleep(ctx, s.sim.MaxLatency*slowLatencyFactor); err != nil {
			return "", err
		}
		return simulatedReply, nil
	default:
		if err := sleep(ctx, s.normalLatency(latencyRoll)); err != nil {
			return "", err
		}
		return simulatedReply, nil
	}
}

func (s *SimulatedLLM) draws() (float64, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rng.Float64(), s.rng.Float64()
}

func (s *SimulatedLLM) normalLatency(roll float64) time.Duration {
	span := s.sim.MaxLatency - s.sim.MinLatency
	return s.sim.MinLatency + time.Duration(roll*float64(span))
}
