package main
import ("database/sql";"fmt";"os";_ "modernc.org/sqlite")
func main(){
 db,_:=sql.Open("sqlite","file:"+os.Args[1]+"?mode=ro")
 defer db.Close()
 rows,err:=db.Query(`SELECT id,COALESCE(type,''),status,title,COALESCE(detail,''),COALESCE(files,''),COALESCE(origin,'') FROM tasks WHERE id=?`,os.Args[2])
 if err!=nil{panic(err)}
 defer rows.Close()
 for rows.Next(){
  var id,typ,st,title,detail,files,origin string
  rows.Scan(&id,&typ,&st,&title,&detail,&files,&origin)
  fmt.Printf("ID:     %s\nTYPE:   %s\nSTATUS: %s\nORIGIN: %s\nFILES:  %s\nTITLE:  %s\n\nDETAIL:\n%s\n",id,typ,st,origin,files,title,detail)
 }
}
