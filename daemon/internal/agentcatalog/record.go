package agentcatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
)

// fileRecord is the on-disk shape of the catalog. It mirrors File but owns its
// JSON tags, so renaming an API field cannot silently change the file format.
type fileRecord struct {
	SchemaVersion int           `json:"schemaVersion"`
	Agents        []agentRecord `json:"agents"`
	Models        []modelRecord `json:"models"`
}

type agentRecord struct {
	ID            string          `json:"id"`
	Binary        string          `json:"binary"`
	RuleSetID     string          `json:"ruleSetId,omitempty"`
	EnclaveTool   string          `json:"enclaveTool,omitempty"`
	Display       string          `json:"display"`
	SessionIDFlag string          `json:"sessionIdFlag"`
	ResumeFlag    string          `json:"resumeFlag"`
	Transcript    bool            `json:"transcript"`
	ListModels    []string        `json:"listModels"`
	Settings      []settingRecord `json:"settings"`
}

type settingRecord struct {
	ID          string   `json:"id"`
	Agent       string   `json:"agent"`
	Field       string   `json:"field"`
	Role        string   `json:"role"`
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Format      string   `json:"format"`
	Choices     []string `json:"choices"`
	Default     string   `json:"default"`
	Delivery    string   `json:"delivery"`
	DeliveryArg string   `json:"deliveryArg"`
	DeliveryKey string   `json:"deliveryKey"`
}

type modelRecord struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	Model string `json:"model"`
}

func recordFile(file File) fileRecord {
	record := fileRecord{SchemaVersion: file.SchemaVersion, Agents: make([]agentRecord, 0, len(file.Agents)), Models: make([]modelRecord, 0, len(file.Models))}
	for _, agent := range file.Agents {
		storedAgent := agentRecord{
			ID: agent.ID, Binary: agent.Binary, RuleSetID: agent.RuleSetID, EnclaveTool: agent.EnclaveTool,
			Display: agent.Display, SessionIDFlag: agent.SessionIDFlag, ResumeFlag: agent.ResumeFlag,
			Transcript: agent.Transcript, ListModels: agent.ListModels, Settings: make([]settingRecord, 0, len(agent.Settings)),
		}
		for _, setting := range agent.Settings {
			storedAgent.Settings = append(storedAgent.Settings, settingRecord{
				ID: setting.ID, Agent: setting.Agent, Field: setting.Field, Role: setting.Role, Type: setting.Type,
				Title: setting.Title, Description: setting.Description, Format: setting.Format, Choices: setting.Choices,
				Default: setting.Default, Delivery: string(setting.Delivery), DeliveryArg: setting.DeliveryArg, DeliveryKey: setting.DeliveryKey,
			})
		}
		record.Agents = append(record.Agents, storedAgent)
	}
	for _, model := range file.Models {
		record.Models = append(record.Models, modelRecord(model))
	}
	return record
}

func (record fileRecord) file() File {
	file := File{SchemaVersion: record.SchemaVersion, Agents: make([]agentspec.Agent, 0, len(record.Agents)), Models: make([]Model, 0, len(record.Models))}
	for _, storedAgent := range record.Agents {
		agent := agentspec.Agent{
			ID: storedAgent.ID, Binary: storedAgent.Binary, RuleSetID: storedAgent.RuleSetID, EnclaveTool: storedAgent.EnclaveTool,
			Display: storedAgent.Display, SessionIDFlag: storedAgent.SessionIDFlag, ResumeFlag: storedAgent.ResumeFlag,
			Transcript: storedAgent.Transcript, ListModels: storedAgent.ListModels, Settings: make([]agentspec.Setting, 0, len(storedAgent.Settings)),
		}
		for _, storedSetting := range storedAgent.Settings {
			agent.Settings = append(agent.Settings, agentspec.Setting{
				ID: storedSetting.ID, Agent: storedSetting.Agent, Field: storedSetting.Field, Role: storedSetting.Role, Type: storedSetting.Type,
				Title: storedSetting.Title, Description: storedSetting.Description, Format: storedSetting.Format, Choices: storedSetting.Choices,
				Default: storedSetting.Default, Delivery: agentspec.Kind(storedSetting.Delivery), DeliveryArg: storedSetting.DeliveryArg, DeliveryKey: storedSetting.DeliveryKey,
			})
		}
		file.Agents = append(file.Agents, agent)
	}
	for _, storedModel := range record.Models {
		file.Models = append(file.Models, Model(storedModel))
	}
	return file
}

// decodeFileRecord parses a catalog file, refusing unknown keys: the file is
// hand-edited, and a misspelled key would otherwise vanish on the next save.
func decodeFileRecord(data []byte) (fileRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record fileRecord
	if err := decoder.Decode(&record); err != nil {
		return fileRecord{}, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fileRecord{}, errors.New("unexpected data after the catalog")
	}
	return record, nil
}
