package worker

import (
 "proxyplatform/internal/config"
 "testing"
 "time"
)

func TestReturnToOriginalBaselineAvoidsSelfRolloverAndKeepsOnlyAuthorizedDrain(t *testing.T) {
 r, original, intermediate := rolloverFixture()
 p:=r.rollovers["a"]
 p.state="warming"
 heldOriginal,err:=r.reserveRoute("policy:p",original)
 if err!=nil {t.Fatal(err)}
 defer heldOriginal.release()
 p.state,p.step,p.serial,p.durableSerial="canary",3,1,1
 heldIntermediate,err:=r.reserveRoute("policy:p",intermediate)
 if err!=nil {t.Fatal(err)}
 defer heldIntermediate.release()
 if heldOriginal.route!=original || heldIntermediate.route!=intermediate {t.Fatal("fixture revision mismatch")}
 previous:=r.current
 r.current=map[string]*engineEntry{"a":r.entries["old"]}
 c:=r.config
 restored:=original.Config
 restored.Previous=[]config.RevisionPermit{{Fingerprint:intermediate.Config.Fingerprint,ValidUntil:intermediate.Config.ValidUntil}}
 c.Routes=[]config.Route{restored}
 r.Restrict(c)
 r.config=c
 r.rolloverDirty.Store(false)
 r.installRolloversLocked(previous,time.Now())
 if r.rollovers["a"]!=nil || !r.rolloverDirty.Load() {t.Fatal("return to baseline retained a self-plan or failed to checkpoint its removal")}
 for range 100 {
  ticket,e:=r.reserveRoute("policy:p",original)
  if e!=nil {t.Fatal(e)}
  if ticket.route!=original {ticket.release();t.Fatal("unproven intermediate admitted")}
  ticket.release()
 }
 if !heldOriginal.allowed() || !heldIntermediate.allowed() || intermediate.References.Load()!=1 {t.Fatal("return interrupted authorized held revisions")}
 c.Routes[0].Previous=nil
 r.Restrict(c)
 if heldIntermediate.allowed() || !heldOriginal.allowed() {t.Fatal("draining intermediate ignored explicit revoke or current baseline was revoked")}
 heldOriginal.release();heldIntermediate.release()
 if original.References.Load()!=0 || intermediate.References.Load()!=0 {t.Fatal("references leaked after return to baseline")}
}
