;; What are the axioms of this thing?
;;
;; We want to implement structured concurrency,
;; in a way that can interoperate with promises.
;;
;; That just means there's a hierarchy of task scopes,
;; and we can't leave a task scope until all the tasks
;; in it are done.
;;
;; When a task scope returns, it must somehow join all
;; its subtasks.  Java has the StructuredTaskScope<T> class
;; for example, with a try (var scope = new StructuredTaskScope())
;; block that demands .join() be called on it before the block
;; can exit.  I believe it's known as a nursery in other systems
;; like Python's trio.
;;
;; We use a prompt to implement this.

(import 
 (scheme base)
 (scheme write)
 (hoot ffi)
 (only (hoot control) make-prompt-tag call-with-prompt abort-to-prompt))

(let ()
  (define task-tag (make-prompt-tag 'task))

  (define-foreign console-log
	"console" "log"
	(ref string) -> none)

  (define-foreign console-group
	"console" "group"
	(ref string) -> none)

  (define-foreign console-group-end
	"console" "groupEnd"
	-> none)

  (define (to-string x)
	(let ((output (open-output-string)))
	  (parameterize ((current-output-port output))
		(display x))
	  (get-output-string output)))
  
  (define (log . items) 
	(console-log (apply string-append (map to-string items))))

  (define info (lambda items 
				 (abort-to-prompt task-tag 'display)
				 (log items)))

  (define (nest id continuation)
	(call-with-prompt task-tag
      continuation
      (lambda (k v)
		(cond
         ((eq? v 'display)
          (begin
			(abort-to-prompt task-tag 'display)
			(console-group (to-string id))
			(nest id k)
			(console-group-end)))))))

  (define (root continuation)
	(call-with-prompt task-tag 
	  continuation
	  (lambda (k v)
		(cond
		 ((eq? v 'display)
		  (begin 
			(console-group "%") 
			(root k)
			(console-group-end)))))))

  (root 
	  (lambda ()
		(nest 'foo (lambda ()
					 (info "hello")
					 (nest 'bar (lambda ()
								  (info "world"))))))))
