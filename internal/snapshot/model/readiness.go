package model

func (m Manifest) PracticalRecoveryReady() bool {
	return m.Validate() == nil
}
