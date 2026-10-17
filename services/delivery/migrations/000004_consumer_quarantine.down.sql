-- Retain parked records and replay audit unless an operator archives them first.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM consumer_quarantine) OR EXISTS(SELECT 1 FROM consumer_replay_audit) THEN
  RAISE EXCEPTION 'consumer quarantine and replay audit must be archived before downgrade';
 END IF;
END $$;
DROP TABLE consumer_replay_audit;
DROP TABLE consumer_quarantine;
